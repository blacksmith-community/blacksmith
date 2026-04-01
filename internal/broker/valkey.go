package broker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/services/common"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/pivotal-cf/brokerapi/v8/domain"
)

var (
	ErrFailedToCreateValkeyACLUser = errors.New("failed to create Valkey ACL user")
	ErrFailedToDeleteValkeyACLUser = errors.New("failed to delete Valkey ACL user")
	ErrValkeyAdminPasswordMissing  = errors.New("admin_password required for Valkey service")
	ErrValkeyHostMissing           = errors.New("host required for Valkey service")
	ErrValkeyACLCommandFailed      = errors.New("Valkey ACL command failed")
)

const (
	valkeyACLRetries  = 3
	valkeyACLBaseWait = 1 * time.Second
	valkeyDialTimeout = 5 * time.Second
	valkeyOpTimeout   = 10 * time.Second
)

// valkeyConnInfo holds the connection details extracted from a credential map.
type valkeyConnInfo struct {
	adminPassword string
	host          string
	port          int
	tlsPort       int
	useTLS        bool
	hosts         []string // non-nil for cluster
}

// IsValkeyService returns true if the credential map indicates a Valkey service.
// Exported for testing.
func IsValkeyService(credMap map[string]interface{}) bool {
	serviceType, ok := credMap["service_type"].(string)
	return ok && serviceType == "valkey"
}

// extractValkeyConnInfo extracts connection details from a credential map.
func extractValkeyConnInfo(credMap map[string]interface{}) (*valkeyConnInfo, error) {
	adminPassword, ok := credMap["admin_password"].(string)
	if !ok || adminPassword == "" {
		return nil, ErrValkeyAdminPasswordMissing
	}

	host, ok := credMap["host"].(string)
	if !ok || host == "" {
		return nil, ErrValkeyHostMissing
	}

	port := extractIntField(credMap, "port", 6379)
	tlsPort := extractIntField(credMap, "tls_port", 0)

	return &valkeyConnInfo{
		adminPassword: adminPassword,
		host:          host,
		port:          port,
		tlsPort:       tlsPort,
		useTLS:        tlsPort > 0,
		hosts:         ExtractHosts(credMap),
	}, nil
}

// extractIntField reads an int from the credential map, handling both int and
// float64 types (YAML/JSON unmarshaling may produce either).
func extractIntField(credMap map[string]interface{}, key string, defaultVal int) int {
	if p, ok := credMap[key].(int); ok && p > 0 {
		return p
	}

	if p, ok := credMap[key].(float64); ok && p > 0 {
		return int(p)
	}

	return defaultVal
}

// createACLUser dispatches to standalone or cluster variant based on
// whether hosts are present in the connection info.
func (b *Broker) createACLUser(ctx context.Context, conn *valkeyConnInfo, username, password string, logger logger.Logger) error {
	if len(conn.hosts) > 0 {
		return b.createValkeyACLUserOnAllNodes(ctx, conn.hosts, conn.port, conn.adminPassword, username, password, conn.useTLS, conn.tlsPort, logger)
	}

	return b.createValkeyACLUser(ctx, conn.host, conn.port, conn.adminPassword, username, password, conn.useTLS, conn.tlsPort, logger)
}

// deleteACLUser dispatches to standalone or cluster variant based on
// whether hosts are present in the connection info.
func (b *Broker) deleteACLUser(ctx context.Context, conn *valkeyConnInfo, username string, logger logger.Logger) error {
	if len(conn.hosts) > 0 {
		return b.deleteValkeyACLUserOnAllNodes(ctx, conn.hosts, conn.port, conn.adminPassword, username, conn.useTLS, conn.tlsPort, logger)
	}

	return b.deleteValkeyACLUser(ctx, conn.host, conn.port, conn.adminPassword, username, conn.useTLS, conn.tlsPort, logger)
}

// processValkeyCredentials creates a per-binding ACL user on the Valkey
// instance and replaces the shared credentials with binding-specific ones.
// For non-Valkey services the credentials are returned unchanged.
func (b *Broker) processValkeyCredentials(ctx context.Context, bindingID string, creds interface{}, logger logger.Logger) (interface{}, error) {
	credMap, ok := creds.(map[string]interface{})
	if !ok || !IsValkeyService(credMap) {
		return creds, nil
	}

	logger.Info("Processing Valkey ACL credentials for binding %s", bindingID)

	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		return nil, err
	}

	dynamicUsername := bindingID
	dynamicPassword := uuid.New().String()

	if err := b.createACLUser(ctx, conn, dynamicUsername, dynamicPassword, logger); err != nil {
		return nil, err
	}

	credMap["username"] = dynamicUsername
	credMap["password"] = dynamicPassword
	credMap["credential_type"] = credentialTypeDynamic

	return creds, nil
}

// handleDynamicValkeyCredentials is the GetBindingCredentials-path handler,
// mirroring handleDynamicRabbitMQCredentials.
func (b *Broker) handleDynamicValkeyCredentials(ctx context.Context, credsMap map[string]interface{}, bindingID string, logger logger.Logger) error {
	conn, err := extractValkeyConnInfo(credsMap)
	if err != nil {
		return err
	}

	dynamicUsername := bindingID
	dynamicPassword := uuid.New().String()

	if err := b.createACLUser(ctx, conn, dynamicUsername, dynamicPassword, logger); err != nil {
		return err
	}

	credsMap["username"] = dynamicUsername
	credsMap["password"] = dynamicPassword
	credsMap["credential_type"] = credentialTypeDynamic

	delete(credsMap, "admin_password")
	delete(credsMap, "service_type")

	return nil
}

// handleValkeyUnbind deletes the per-binding ACL user from the Valkey instance.
func (b *Broker) handleValkeyUnbind(ctx context.Context, instanceID, bindingID string, details domain.UnbindDetails, logger logger.Logger) error {
	logger.Info("Processing unbind for Valkey service")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)
		return err
	}

	credMap, err := b.getValkeyCredentials(ctx, instanceID, &plan, logger)
	if err != nil {
		return err
	}

	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		return err
	}

	return b.deleteACLUser(ctx, conn, bindingID, logger)
}

// getValkeyCredentials retrieves the raw credential map for a Valkey instance.
func (b *Broker) getValkeyCredentials(_ context.Context, instanceID string, plan *services.Plan, logger logger.Logger) (map[string]interface{}, error) {
	logger.Debug("Retrieving admin credentials for Valkey instance")

	deploymentManifest := b.getDeploymentManifestFromVault(instanceID, logger)

	creds, err := manifest.GetCreds(instanceID, *plan, b.BOSH, deploymentManifest, logger)
	if err != nil {
		logger.Error("Failed to retrieve credentials: %s", err)
		return nil, fmt.Errorf("failed to get Valkey credentials: %w", err)
	}

	credMap, ok := creds.(map[string]interface{})
	if !ok {
		logger.Error("Invalid creds type: %T", creds)
		return nil, fmt.Errorf("%w: %T", ErrInvalidCredsType, creds)
	}

	return credMap, nil
}

// createValkeyACLUser creates a single ACL user on one Valkey node.
func (b *Broker) createValkeyACLUser(ctx context.Context, host string, port int, adminPassword, username, password string, useTLS bool, tlsPort int, logger logger.Logger) error {
	logger.Info("Creating Valkey ACL user %s on %s:%d", username, host, port)

	return common.WithRetry(ctx, func() error {
		client, err := connectToValkeyAdmin(ctx, host, port, adminPassword, useTLS, tlsPort)
		if err != nil {
			return fmt.Errorf("%w: connection failed: %w", ErrFailedToCreateValkeyACLUser, err)
		}
		defer client.Close()

		result := client.Do(ctx, "ACL", "SETUSER", username, "on", ">"+password, "~*", "&*", "+@all", "-@admin")
		if result.Err() != nil {
			logger.Error("ACL SETUSER failed: %s", result.Err())
			return fmt.Errorf("%w: SETUSER: %w", ErrValkeyACLCommandFailed, result.Err())
		}

		if err := aclSave(ctx, client, logger); err != nil {
			return err
		}

		logger.Debug("Successfully created ACL user %s", username)

		return nil
	}, valkeyACLRetries, valkeyACLBaseWait)
}

// createValkeyACLUserOnAllNodes creates the ACL user on every cluster node.
func (b *Broker) createValkeyACLUserOnAllNodes(ctx context.Context, hosts []string, port int, adminPassword, username, password string, useTLS bool, tlsPort int, logger logger.Logger) error {
	logger.Info("Creating Valkey ACL user %s on %d cluster nodes", username, len(hosts))

	var errs []string

	for _, host := range hosts {
		if err := b.createValkeyACLUser(ctx, host, port, adminPassword, username, password, useTLS, tlsPort, logger); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", host, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrFailedToCreateValkeyACLUser, strings.Join(errs, "; "))
	}

	return nil
}

// deleteValkeyACLUser deletes an ACL user from one Valkey node.
// ACL DELUSER is naturally idempotent — it returns 0 for non-existent users
// without raising an error.
func (b *Broker) deleteValkeyACLUser(ctx context.Context, host string, port int, adminPassword, username string, useTLS bool, tlsPort int, logger logger.Logger) error {
	logger.Info("Deleting Valkey ACL user %s on %s:%d", username, host, port)

	return common.WithRetry(ctx, func() error {
		client, err := connectToValkeyAdmin(ctx, host, port, adminPassword, useTLS, tlsPort)
		if err != nil {
			return fmt.Errorf("%w: connection failed: %w", ErrFailedToDeleteValkeyACLUser, err)
		}
		defer client.Close()

		result := client.Do(ctx, "ACL", "DELUSER", username)
		if result.Err() != nil {
			logger.Error("ACL DELUSER failed: %s", result.Err())
			return fmt.Errorf("%w: DELUSER: %w", ErrValkeyACLCommandFailed, result.Err())
		}

		if err := aclSave(ctx, client, logger); err != nil {
			return err
		}

		logger.Debug("Successfully deleted ACL user %s", username)

		return nil
	}, valkeyACLRetries, valkeyACLBaseWait)
}

// deleteValkeyACLUserOnAllNodes deletes the ACL user from every cluster node.
func (b *Broker) deleteValkeyACLUserOnAllNodes(ctx context.Context, hosts []string, port int, adminPassword, username string, useTLS bool, tlsPort int, logger logger.Logger) error {
	logger.Info("Deleting Valkey ACL user %s from %d cluster nodes", username, len(hosts))

	var errs []string

	for _, host := range hosts {
		if err := b.deleteValkeyACLUser(ctx, host, port, adminPassword, username, useTLS, tlsPort, logger); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", host, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrFailedToDeleteValkeyACLUser, strings.Join(errs, "; "))
	}

	return nil
}

// aclSave persists the current ACL state to the aclfile.
func aclSave(ctx context.Context, client *redis.Client, logger logger.Logger) error {
	result := client.Do(ctx, "ACL", "SAVE")
	if result.Err() != nil {
		logger.Error("ACL SAVE failed: %s", result.Err())
		return fmt.Errorf("%w: SAVE: %w", ErrValkeyACLCommandFailed, result.Err())
	}

	return nil
}

// connectToValkeyAdmin creates a short-lived Redis client authenticated as the
// default (admin) user. The caller is responsible for closing the client.
func connectToValkeyAdmin(ctx context.Context, host string, port int, adminPassword string, useTLS bool, tlsPort int) (*redis.Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)

	opts := &redis.Options{
		Addr:         addr,
		Password:     adminPassword,
		DB:           0,
		DialTimeout:  valkeyDialTimeout,
		ReadTimeout:  valkeyOpTimeout,
		WriteTimeout: valkeyOpTimeout,
	}

	if useTLS && tlsPort > 0 {
		opts.Addr = fmt.Sprintf("%s:%d", host, tlsPort)
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 - Internal BOSH network
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, valkeyDialTimeout)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("valkey ping failed at %s: %w", opts.Addr, err)
	}

	return client, nil
}

// ExtractHosts returns the list of cluster node IPs from the credential map,
// or nil for standalone instances. Exported for testing.
func ExtractHosts(credMap map[string]interface{}) []string {
	hostsRaw, ok := credMap["hosts"]
	if !ok {
		return nil
	}

	switch v := hostsRaw.(type) {
	case []interface{}:
		hosts := make([]string, 0, len(v))

		for _, h := range v {
			if s, ok := h.(string); ok {
				hosts = append(hosts, s)
			}
		}

		if len(hosts) > 0 {
			return hosts
		}
	case []string:
		if len(v) > 0 {
			return v
		}
	}

	return nil
}

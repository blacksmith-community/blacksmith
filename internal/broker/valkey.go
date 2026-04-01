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

// IsValkeyService returns true if the credential map indicates a Valkey service.
// Exported for testing.
func IsValkeyService(credMap map[string]interface{}) bool {
	serviceType, ok := credMap["service_type"].(string)
	return ok && serviceType == "valkey"
}

// processValkeyCredentials creates a per-binding ACL user on the Valkey
// instance and replaces the shared credentials with binding-specific ones.
// For non-Valkey services the credentials are returned unchanged.
func (b *Broker) processValkeyCredentials(ctx context.Context, bindingID string, creds interface{}, logger logger.Logger) (interface{}, error) {
	credMap, ok := creds.(map[string]interface{})
	if !ok {
		return creds, nil
	}

	if !IsValkeyService(credMap) {
		return creds, nil
	}

	logger.Info("Processing Valkey ACL credentials for binding %s", bindingID)

	adminPassword, ok := credMap["admin_password"].(string)
	if !ok || adminPassword == "" {
		return nil, ErrValkeyAdminPasswordMissing
	}

	host, ok := credMap["host"].(string)
	if !ok || host == "" {
		return nil, ErrValkeyHostMissing
	}

	port := 6379
	if p, ok := credMap["port"].(int); ok && p > 0 {
		port = p
	} else if p, ok := credMap["port"].(float64); ok && p > 0 {
		port = int(p)
	}

	tlsPort := 0
	if p, ok := credMap["tls_port"].(int); ok {
		tlsPort = p
	} else if p, ok := credMap["tls_port"].(float64); ok {
		tlsPort = int(p)
	}

	useTLS := tlsPort > 0

	dynamicUsername := bindingID
	dynamicPassword := uuid.New().String()

	// Determine standalone vs cluster
	hosts := ExtractHosts(credMap)
	if len(hosts) > 0 {
		err := b.createValkeyACLUserOnAllNodes(ctx, hosts, port, adminPassword, dynamicUsername, dynamicPassword, useTLS, tlsPort, logger)
		if err != nil {
			return nil, err
		}
	} else {
		err := b.createValkeyACLUser(ctx, host, port, adminPassword, dynamicUsername, dynamicPassword, useTLS, tlsPort, logger)
		if err != nil {
			return nil, err
		}
	}

	// Replace credentials with per-binding values
	credMap["username"] = dynamicUsername
	credMap["password"] = dynamicPassword
	credMap["credential_type"] = credentialTypeDynamic

	return creds, nil
}

// handleDynamicValkeyCredentials is the GetBindingCredentials-path handler,
// mirroring handleDynamicRabbitMQCredentials.
func (b *Broker) handleDynamicValkeyCredentials(ctx context.Context, credsMap map[string]interface{}, bindingID string, logger logger.Logger) error {
	adminPassword, ok := credsMap["admin_password"].(string)
	if !ok || adminPassword == "" {
		return ErrValkeyAdminPasswordMissing
	}

	host, ok := credsMap["host"].(string)
	if !ok || host == "" {
		return ErrValkeyHostMissing
	}

	port := 6379
	if p, ok := credsMap["port"].(int); ok && p > 0 {
		port = p
	} else if p, ok := credsMap["port"].(float64); ok && p > 0 {
		port = int(p)
	}

	tlsPort := 0
	if p, ok := credsMap["tls_port"].(int); ok {
		tlsPort = p
	} else if p, ok := credsMap["tls_port"].(float64); ok {
		tlsPort = int(p)
	}

	useTLS := tlsPort > 0

	dynamicUsername := bindingID
	dynamicPassword := uuid.New().String()

	hosts := ExtractHosts(credsMap)
	if len(hosts) > 0 {
		if err := b.createValkeyACLUserOnAllNodes(ctx, hosts, port, adminPassword, dynamicUsername, dynamicPassword, useTLS, tlsPort, logger); err != nil {
			return err
		}
	} else {
		if err := b.createValkeyACLUser(ctx, host, port, adminPassword, dynamicUsername, dynamicPassword, useTLS, tlsPort, logger); err != nil {
			return err
		}
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

	adminPassword, ok := credMap["admin_password"].(string)
	if !ok || adminPassword == "" {
		return ErrValkeyAdminPasswordMissing
	}

	host, ok := credMap["host"].(string)
	if !ok || host == "" {
		return ErrValkeyHostMissing
	}

	port := 6379
	if p, ok := credMap["port"].(int); ok && p > 0 {
		port = p
	} else if p, ok := credMap["port"].(float64); ok && p > 0 {
		port = int(p)
	}

	tlsPort := 0
	if p, ok := credMap["tls_port"].(int); ok {
		tlsPort = p
	} else if p, ok := credMap["tls_port"].(float64); ok {
		tlsPort = int(p)
	}

	useTLS := tlsPort > 0

	hosts := ExtractHosts(credMap)
	if len(hosts) > 0 {
		return b.deleteValkeyACLUserOnAllNodes(ctx, hosts, port, adminPassword, bindingID, useTLS, tlsPort, logger)
	}

	return b.deleteValkeyACLUser(ctx, host, port, adminPassword, bindingID, useTLS, tlsPort, logger)
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

		// ACL SETUSER <username> on ><password> ~* &* +@all -@admin
		result := client.Do(ctx, "ACL", "SETUSER", username, "on", ">"+password, "~*", "&*", "+@all", "-@admin")
		if result.Err() != nil {
			logger.Error("ACL SETUSER failed: %s", result.Err())
			return fmt.Errorf("%w: SETUSER: %w", ErrValkeyACLCommandFailed, result.Err())
		}

		// ACL SAVE to persist
		saveResult := client.Do(ctx, "ACL", "SAVE")
		if saveResult.Err() != nil {
			logger.Error("ACL SAVE failed: %s", saveResult.Err())
			return fmt.Errorf("%w: SAVE: %w", ErrValkeyACLCommandFailed, saveResult.Err())
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
			// User not found is success (idempotent)
			if strings.Contains(result.Err().Error(), "ERR The user") {
				logger.Info("ACL user %s does not exist, treating as success", username)
				return nil
			}

			logger.Error("ACL DELUSER failed: %s", result.Err())

			return fmt.Errorf("%w: DELUSER: %w", ErrValkeyACLCommandFailed, result.Err())
		}

		saveResult := client.Do(ctx, "ACL", "SAVE")
		if saveResult.Err() != nil {
			logger.Error("ACL SAVE failed: %s", saveResult.Err())
			return fmt.Errorf("%w: SAVE: %w", ErrValkeyACLCommandFailed, saveResult.Err())
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
		return nil, fmt.Errorf("Valkey ping failed at %s: %w", opts.Addr, err)
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

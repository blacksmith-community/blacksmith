package broker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/services/common"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
)

var (
	ErrFailedToCreateValkeyACLUser = errors.New("failed to create Valkey ACL user")
	ErrFailedToDeleteValkeyACLUser = errors.New("failed to delete Valkey ACL user")
	ErrValkeyAdminPasswordMissing  = errors.New("admin_password required for Valkey service")
	ErrValkeyHostMissing           = errors.New("host required for Valkey service")
)

const (
	valkeyDialTimeout = 5 * time.Second
	valkeyOpTimeout   = 10 * time.Second
)

// Retry settings for Valkey ACL operations (overridden in tests).
var (
	valkeyACLRetries  = 3               //nolint:gochecknoglobals // overridden in tests
	valkeyACLBaseWait = 1 * time.Second //nolint:gochecknoglobals // overridden in tests
)

// Steps of an admin exchange with one Valkey node. Errors and logs name the
// step so an operator can tell a refused connection from a rejected command.
const (
	valkeyStepDial    = "dial"
	valkeyStepAuth    = "AUTH"
	valkeyStepPing    = "PING"
	valkeyStepSetUser = "ACL SETUSER"
	valkeyStepDelUser = "ACL DELUSER"
	valkeyStepACLSave = "ACL SAVE"
)

// valkeyStepError reports which step of an admin exchange with one Valkey
// node failed, the address it targeted, and a likely cause when the failure
// points at one. The platform shows its message to operators verbatim, so it
// must never carry a password.
type valkeyStepError struct {
	step   string
	addr   string
	useTLS bool
	err    error
	// serverReply is true when the node answered the step with an error
	// reply, as opposed to a refused dial, a reset, or a timeout.
	serverReply bool
}

func (e *valkeyStepError) Error() string {
	transport := "plaintext"
	if e.useTLS {
		transport = "TLS"
	}

	msg := fmt.Sprintf("valkey %s %s (%s) failed: %s", e.step, e.addr, transport, e.err)

	cause := valkeyLikelyCause(e.step, e.err)
	if cause != "" {
		msg = strings.TrimSuffix(msg, ".") + "; likely cause: " + cause
	}

	return msg
}

func (e *valkeyStepError) Unwrap() error {
	return e.err
}

// valkeyLikelyCause returns a short hint for failures whose cause is known,
// or "" when the error says nothing more than its own text.
func valkeyLikelyCause(step string, err error) string {
	var redisErr redis.Error
	if errors.As(err, &redisErr) && strings.HasPrefix(redisErr.Error(), "MISCONF") {
		return "the Valkey node cannot persist to disk (RDB snapshots or AOF writes are failing), " +
			"so it refuses writes and PING; check the node's server log and free space on its persistent disk"
	}

	switch step {
	case valkeyStepDial:
		return "the node is down, unreachable from the broker, or not listening on this port; " +
			"check the instance's VMs and the network path from the broker"
	case valkeyStepAuth:
		return "the instance's admin_password does not match the password the node requires"
	}

	return ""
}

// valkeyNonRetryablePrefixes are error reply codes that repeating the same
// command cannot fix: a node that cannot persist to disk, a wrong admin
// password, a missing login, or a user without permission.
var valkeyNonRetryablePrefixes = []string{"MISCONF", "WRONGPASS", "NOAUTH", "NOPERM"} //nolint:gochecknoglobals // constant list

// isNonRetryableValkeyError reports whether err is a server reply that a retry
// cannot change. Dial failures, resets, and timeouts are not, because a node
// that is restarting can recover.
func isNonRetryableValkeyError(err error) bool {
	var stepErr *valkeyStepError
	if errors.As(err, &stepErr) {
		if stepErr.step == valkeyStepAuth && stepErr.serverReply {
			return true
		}

		// The step error may hold a redacted copy of the reply, so read its text.
		return hasNonRetryablePrefix(stepErr.err.Error())
	}

	var redisErr redis.Error
	if errors.As(err, &redisErr) {
		return hasNonRetryablePrefix(redisErr.Error())
	}

	return false
}

func hasNonRetryablePrefix(msg string) bool {
	for _, prefix := range valkeyNonRetryablePrefixes {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}

	return false
}

// redactedError carries an error message with a secret removed. It drops the
// original error from the chain, since that error's text holds the secret.
type redactedError struct {
	msg string
}

func (e *redactedError) Error() string {
	return e.msg
}

// redactSecret removes every occurrence of secret from err's message. Valkey
// echoes a rejected ACL SETUSER modifier in its reply, and the password
// modifier carries the binding's password.
func redactSecret(err error, secret string) error {
	if secret == "" || !strings.Contains(err.Error(), secret) {
		return err
	}

	return &redactedError{msg: strings.ReplaceAll(err.Error(), secret, "[REDACTED]")}
}

// valkeyConnInfo holds the connection details extracted from a credential map.
type valkeyConnInfo struct {
	adminPassword string
	host          string
	port          int
	tlsPort       int
	useTLS        bool
	hosts         []string // non-nil for cluster
}

// addr returns the address the broker dials on host: the TLS port when the
// instance offers TLS, the plaintext port otherwise.
func (c *valkeyConnInfo) addr(host string) string {
	if c.useTLS {
		return host + ":" + strconv.Itoa(c.tlsPort)
	}

	return host + ":" + strconv.Itoa(c.port)
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
func (b *Broker) createACLUser(ctx context.Context, instanceID string, conn *valkeyConnInfo, username, password string, logger logger.Logger) error {
	if len(conn.hosts) > 0 {
		return b.createValkeyACLUserOnAllNodes(ctx, instanceID, conn, username, password, logger)
	}

	err := b.createValkeyACLUser(ctx, instanceID, conn, conn.host, username, password, logger)
	if err != nil {
		return fmt.Errorf("%w %s: %w", ErrFailedToCreateValkeyACLUser, username, err)
	}

	return nil
}

// deleteACLUser dispatches to standalone or cluster variant based on
// whether hosts are present in the connection info.
func (b *Broker) deleteACLUser(ctx context.Context, instanceID string, conn *valkeyConnInfo, username string, logger logger.Logger) error {
	if len(conn.hosts) > 0 {
		return b.deleteValkeyACLUserOnAllNodes(ctx, instanceID, conn, username, logger)
	}

	err := b.deleteValkeyACLUser(ctx, instanceID, conn, conn.host, username, logger)
	if err != nil {
		return fmt.Errorf("%w %s: %w", ErrFailedToDeleteValkeyACLUser, username, err)
	}

	return nil
}

// processValkeyCredentials creates a per-binding ACL user on the Valkey
// instance and replaces the shared credentials with binding-specific ones.
// For non-Valkey services the credentials are returned unchanged.
func (b *Broker) processValkeyCredentials(ctx context.Context, instanceID, bindingID string, creds interface{}, logger logger.Logger) (interface{}, error) {
	credMap, ok := creds.(map[string]interface{})
	if !ok || !IsValkeyService(credMap) {
		return creds, nil
	}

	logger.Info("Processing Valkey ACL credentials for binding %s", bindingID)

	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		logger.Error("Cannot create Valkey ACL user for instance %s binding %s: %s", instanceID, bindingID, err)

		return nil, err
	}

	dynamicUsername := bindingID
	dynamicPassword := uuid.New().String()

	err = b.createACLUser(ctx, instanceID, conn, dynamicUsername, dynamicPassword, logger)
	if err != nil {
		return nil, err
	}

	credMap["username"] = dynamicUsername
	credMap["password"] = dynamicPassword
	credMap["credential_type"] = credentialTypeDynamic

	return creds, nil
}

// handleDynamicValkeyCredentials is the GetBindingCredentials-path handler,
// mirroring handleDynamicRabbitMQCredentials.
func (b *Broker) handleDynamicValkeyCredentials(ctx context.Context, instanceID string, credsMap map[string]interface{}, bindingID string, logger logger.Logger) error {
	conn, err := extractValkeyConnInfo(credsMap)
	if err != nil {
		logger.Error("Cannot create Valkey ACL user for instance %s binding %s: %s", instanceID, bindingID, err)

		return err
	}

	dynamicUsername := bindingID
	dynamicPassword := uuid.New().String()

	err = b.createACLUser(ctx, instanceID, conn, dynamicUsername, dynamicPassword, logger)
	if err != nil {
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
func (b *Broker) handleValkeyUnbind(ctx context.Context, instanceID, bindingID string, details osbapi.UnbindRequest, logger logger.Logger) error {
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

	// Symmetry with the bind path (processDynamicCredentials): dynamic per-binding users are
	// only ever created when the plan's credentials carry the ACL trigger fields
	// (service_type: valkey + admin_password). A classic plan (shared-password credentials)
	// never minted a user for this binding, so there is nothing to delete -- and we could not
	// even connect as admin. Succeed as a no-op instead of failing the unbind.
	if !IsValkeyService(credMap) {
		logger.Info("Plan credentials carry no ACL trigger fields (classic plan) - no per-binding user to delete for binding %s", bindingID)
		return nil
	}

	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		logger.Error("Cannot delete Valkey ACL user for instance %s binding %s: %s", instanceID, bindingID, err)

		return err
	}

	return b.deleteACLUser(ctx, instanceID, conn, bindingID, logger)
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

// withValkeyACLRetry runs one ACL operation against one node under
// common.WithRetry, logging every failed attempt with the instance, binding,
// address, transport, and attempt number before the next one starts.
func withValkeyACLRetry(ctx context.Context, action, instanceID, bindingID string, conn *valkeyConnInfo, host string, logger logger.Logger, attemptFn func() error) error {
	attempt := 0
	attempts := valkeyACLRetries + 1

	//nolint:wrapcheck // callers wrap the result with the operation's sentinel
	return common.WithRetry(ctx, func() error {
		attempt++

		err := attemptFn()
		if err != nil {
			if isNonRetryableValkeyError(err) {
				logger.Error("Failed to %s Valkey ACL user for instance %s binding %s at %s (tls=%t), attempt %d of %d, not retrying because the node's reply will not change: %s",
					action, instanceID, bindingID, conn.addr(host), conn.useTLS, attempt, attempts, err)

				return common.NewRetryableError(err, false, 0)
			}

			logger.Error("Failed to %s Valkey ACL user for instance %s binding %s at %s (tls=%t), attempt %d of %d: %s",
				action, instanceID, bindingID, conn.addr(host), conn.useTLS, attempt, attempts, err)
		}

		return err
	}, valkeyACLRetries, valkeyACLBaseWait)
}

// createValkeyACLUser creates a single ACL user on one Valkey node.
func (b *Broker) createValkeyACLUser(ctx context.Context, instanceID string, conn *valkeyConnInfo, host, username, password string, logger logger.Logger) error {
	logger.Info("Creating Valkey ACL user %s on %s (tls=%t)", username, conn.addr(host), conn.useTLS)

	err := withValkeyACLRetry(ctx, "create", instanceID, username, conn, host, logger, func() error {
		client, err := connectToValkeyAdmin(ctx, conn, host)
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck // short-lived client, nothing to recover on close

		err = client.Do(ctx, "ACL", "SETUSER", username, "on", ">"+password, "~*", "&*", "+@all", "-@admin").Err()
		if err != nil {
			return &valkeyStepError{step: valkeyStepSetUser, addr: conn.addr(host), useTLS: conn.useTLS, err: redactSecret(err, password)}
		}

		return aclSave(ctx, client, conn, host)
	})
	if err != nil {
		return err
	}

	logger.Debug("Successfully created ACL user %s", username)

	return nil
}

// createValkeyACLUserOnAllNodes creates the ACL user on every cluster node.
func (b *Broker) createValkeyACLUserOnAllNodes(ctx context.Context, instanceID string, conn *valkeyConnInfo, username, password string, logger logger.Logger) error {
	logger.Info("Creating Valkey ACL user %s on %d cluster nodes", username, len(conn.hosts))

	var errs []string

	for _, host := range conn.hosts {
		err := b.createValkeyACLUser(ctx, instanceID, conn, host, username, password, logger)
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w %s on %d of %d cluster nodes: %s",
			ErrFailedToCreateValkeyACLUser, username, len(errs), len(conn.hosts), strings.Join(errs, "; "))
	}

	return nil
}

// deleteValkeyACLUser deletes an ACL user from one Valkey node.
// ACL DELUSER is naturally idempotent — it returns 0 for non-existent users
// without raising an error.
func (b *Broker) deleteValkeyACLUser(ctx context.Context, instanceID string, conn *valkeyConnInfo, host, username string, logger logger.Logger) error {
	logger.Info("Deleting Valkey ACL user %s on %s (tls=%t)", username, conn.addr(host), conn.useTLS)

	err := withValkeyACLRetry(ctx, "delete", instanceID, username, conn, host, logger, func() error {
		client, err := connectToValkeyAdmin(ctx, conn, host)
		if err != nil {
			return err
		}
		defer client.Close() //nolint:errcheck // short-lived client, nothing to recover on close

		err = client.Do(ctx, "ACL", "DELUSER", username).Err()
		if err != nil {
			return &valkeyStepError{step: valkeyStepDelUser, addr: conn.addr(host), useTLS: conn.useTLS, err: err}
		}

		return aclSave(ctx, client, conn, host)
	})
	if err != nil {
		return err
	}

	logger.Debug("Successfully deleted ACL user %s", username)

	return nil
}

// deleteValkeyACLUserOnAllNodes deletes the ACL user from every cluster node.
func (b *Broker) deleteValkeyACLUserOnAllNodes(ctx context.Context, instanceID string, conn *valkeyConnInfo, username string, logger logger.Logger) error {
	logger.Info("Deleting Valkey ACL user %s from %d cluster nodes", username, len(conn.hosts))

	var errs []string

	for _, host := range conn.hosts {
		err := b.deleteValkeyACLUser(ctx, instanceID, conn, host, username, logger)
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w %s on %d of %d cluster nodes: %s",
			ErrFailedToDeleteValkeyACLUser, username, len(errs), len(conn.hosts), strings.Join(errs, "; "))
	}

	return nil
}

// aclSave persists the current ACL state to the aclfile.
func aclSave(ctx context.Context, client *redis.Client, conn *valkeyConnInfo, host string) error {
	err := client.Do(ctx, "ACL", "SAVE").Err()
	if err != nil {
		return &valkeyStepError{step: valkeyStepACLSave, addr: conn.addr(host), useTLS: conn.useTLS, err: err}
	}

	return nil
}

// connectToValkeyAdmin creates a short-lived Redis client on host,
// authenticated as the default (admin) user, and checks it with PING. A
// failure comes back as a *valkeyStepError naming the dial, AUTH, or PING
// step. The caller is responsible for closing the client.
func connectToValkeyAdmin(ctx context.Context, conn *valkeyConnInfo, host string) (*redis.Client, error) {
	addr := conn.addr(host)

	// The password goes through OnConnect rather than Options.Password so a
	// rejected AUTH can be told apart from a refused dial or a failed PING;
	// go-redis returns all three from the same Ping call. Its own retries are
	// off so that one Ping makes exactly one dial; WithRetry in the callers
	// retries the whole exchange instead.
	var (
		connected bool
		authErr   error
	)

	opts := &redis.Options{
		Addr:         addr,
		DB:           0,
		MaxRetries:   -1,
		DialTimeout:  valkeyDialTimeout,
		ReadTimeout:  valkeyOpTimeout,
		WriteTimeout: valkeyOpTimeout,
		OnConnect: func(ctx context.Context, cn *redis.Conn) error {
			connected = true

			authErr = cn.Auth(ctx, conn.adminPassword).Err()

			return authErr //nolint:wrapcheck // go-redis unwraps OnConnect errors; the caller classifies authErr itself
		},
	}

	if conn.useTLS {
		opts.TLSConfig = &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 - Internal BOSH network
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
		}
	}

	client := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(ctx, valkeyDialTimeout)
	defer cancel()

	err := client.Ping(pingCtx).Err()
	if err != nil {
		_ = client.Close()

		step := valkeyStepPing
		serverReply := false

		switch {
		case authErr != nil:
			step = valkeyStepAuth

			var replyErr redis.Error

			serverReply = errors.As(authErr, &replyErr)
			err = redactSecret(authErr, conn.adminPassword)
		case !connected:
			step = valkeyStepDial
		}

		return nil, &valkeyStepError{step: step, addr: addr, useTLS: conn.useTLS, err: err, serverReply: serverReply}
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

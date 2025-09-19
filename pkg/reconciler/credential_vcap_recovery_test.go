package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

// Static errors for VCAP recovery test err113 compliance.
var (
	ErrCFAPIUnavailable   = errors.New("CF API unavailable")
	ErrCFEnvEndpointError = errors.New("CF env endpoint error")
)

// Mock CF manager implementing CFManagerInterface.
type mockCFManager struct {
	// instanceID -> []appGUID
	apps map[string][]string
	// appGUID -> env (must contain vcap_services)
	env map[string]map[string]interface{}
	// optional errors per method key
	findAppsErr map[string]error // keyed by instanceID
	getEnvErr   map[string]error // keyed by appGUID
}

func newMockCFManager() *mockCFManager {
	return &mockCFManager{
		apps:        make(map[string][]string),
		env:         make(map[string]map[string]interface{}),
		findAppsErr: make(map[string]error),
		getEnvErr:   make(map[string]error),
	}
}

func (m *mockCFManager) FindAppsByServiceInstance(ctx context.Context, serviceInstanceGUID string) ([]string, error) {
	if err, ok := m.findAppsErr[serviceInstanceGUID]; ok {
		return nil, err
	}

	return m.apps[serviceInstanceGUID], nil
}

func (m *mockCFManager) GetAppEnvironmentWithVCAP(ctx context.Context, appGUID string) (map[string]interface{}, error) {
	if err, ok := m.getEnvErr[appGUID]; ok {
		return nil, err
	}

	if env, ok := m.env[appGUID]; ok {
		return env, nil
	}

	return map[string]interface{}{}, nil
}

// Simple in-memory vault mock implementing VaultInterface.
type vcapTestVault struct {
	data  map[string]map[string]interface{}
	calls []string
}

func newVCAPTestVault() *vcapTestVault {
	return &vcapTestVault{data: make(map[string]map[string]interface{}), calls: []string{}}
}

func (v *vcapTestVault) Get(path string) (map[string]interface{}, error) {
	v.calls = append(v.calls, "GET:"+path)
	if d, ok := v.data[path]; ok {
		return d, nil
	}

	return nil, ErrNotFound
}

func (v *vcapTestVault) Put(path string, secret map[string]interface{}) error {
	v.calls = append(v.calls, "PUT:"+path)
	v.data[path] = secret

	return nil
}

func (v *vcapTestVault) GetSecret(path string) (map[string]interface{}, error) { return v.Get(path) }
func (v *vcapTestVault) SetSecret(path string, secret map[string]interface{}) error {
	return v.Put(path, secret)
}
func (v *vcapTestVault) DeleteSecret(path string) error {
	delete(v.data, path)

	return nil
}
func (v *vcapTestVault) ListSecrets(path string) ([]string, error) { return []string{}, nil }

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestVCAPRecovery_RabbitMQ_Success(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "svc-inst-uuid-1111-2222-3333-444444444444"
	appGUID := "app-guid-1"

	// Build vcap_services for rabbitmq
	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"rabbitmq": []interface{}{
				map[string]interface{}{
					"instance_guid": instanceID,
					"credentials": map[string]interface{}{
						"username": "admin",
						"password": "secret",
						"protocols": map[string]interface{}{
							"amqp": map[string]interface{}{
								"host":     "rabbit.internal",
								"port":     5672,
								"username": "admin",
								"password": "secret",
								"vhost":    "/",
							},
						},
					},
				},
			},
		},
	}

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify credentials stored
	creds, err := vault.Get(instanceID + "/credentials")
	if err != nil {
		t.Fatalf("stored credentials not found: %v", err)
	}
	// Expect normalized fields
	if creds["host"] != "rabbit.internal" {
		t.Errorf("expected host rabbit.internal, got %v", creds["host"])
	}

	if creds["port"] != 5672 {
		t.Errorf("expected port 5672, got %v", creds["port"])
	}

	if creds["username"] != "admin" || creds["password"] != "secret" {
		t.Errorf("expected username/password normalized, got %v/%v", creds["username"], creds["password"])
	}

	// Verify metadata updated
	meta, err := vault.Get(instanceID + "/metadata")
	if err != nil {
		t.Fatalf("expected metadata to be stored: %v", err)
	}

	if meta["credentials_recovered_from"] != "vcap_services" {
		t.Errorf("expected credentials_recovered_from=vcap_services, got %v", meta["credentials_recovered_from"])
	}

	if meta["credentials_source_app"] != appGUID {
		t.Errorf("expected credentials_source_app=%s, got %v", appGUID, meta["credentials_source_app"])
	}
}

//nolint:funlen
func TestVCAPRecovery_MetadataTimestampAndFields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "svc-inst-meta-uuid-0000-1111-2222-333333333333"
	appGUID := "app-guid-meta"

	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"rabbitmq": []interface{}{
				map[string]interface{}{
					"instance_guid": instanceID,
					"credentials": map[string]interface{}{
						"username": "admin",
						"password": "secret",
						"protocols": map[string]interface{}{
							"amqp": map[string]interface{}{"host": "h", "port": 5672, "username": "admin", "password": "secret", "vhost": "/"},
						},
					},
				},
			},
		},
	}

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	before := time.Now().Add(-2 * time.Second)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	after := time.Now().Add(2 * time.Second)

	meta, err := vault.Get(instanceID + "/metadata")
	if err != nil {
		t.Fatalf("expected metadata to be stored: %v", err)
	}
	// Check timestamp present and within reasonable range
	recoveredAtStr, ok := meta["credentials_recovered_at"].(string)
	if !ok || recoveredAtStr == "" {
		t.Fatalf("expected credentials_recovered_at timestamp, got %v", meta["credentials_recovered_at"])
	}

	recoveredAt, err := time.Parse(time.RFC3339, recoveredAtStr)
	if err != nil {
		t.Fatalf("credentials_recovered_at not RFC3339: %v", err)
	}

	if recoveredAt.Before(before) || recoveredAt.After(after) {
		t.Errorf("credentials_recovered_at out of expected range: %v not between %v and %v", recoveredAt, before, after)
	}

	// Check recovery method field
	if meta["credentials_recovery_method"] != "cf_app_environment" {
		t.Errorf("expected credentials_recovery_method=cf_app_environment, got %v", meta["credentials_recovery_method"])
	}
}

func TestVCAPRecovery_SkipsWhenCredentialsExist(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "existing-creds-inst"
	appGUID := "app-guid-2"

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = map[string]interface{}{"vcap_services": map[string]interface{}{}}

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	// Pre-populate credentials
	original := map[string]interface{}{"pre_existing": true}
	_ = vault.Put(instanceID+"/credentials", original)

	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Ensure credentials unchanged (no overwrite)
	creds, _ := vault.Get(instanceID + "/credentials")
	if !reflect.DeepEqual(creds, original) {
		t.Errorf("credentials should be unchanged; got %#v", creds)
	}
	// Ensure no extra PUT to credentials beyond initial
	putCount := 0

	for _, c := range vault.calls {
		if c == "PUT:"+instanceID+"/credentials" {
			putCount++
		}
	}

	if putCount != 1 {
		t.Errorf("expected exactly 1 PUT to credentials (pre-populate), got %d", putCount)
	}
}

func TestVCAPRecovery_NoAppsBound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "no-apps"
	cfManager := newMockCFManager() // no apps configured
	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err == nil {
		t.Fatalf("expected error for no apps bound, got nil")
	}
}

func TestVCAPRecovery_ServiceInstanceNotInVCAP(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "missing-in-vcap"
	appGUID := "app-guid-3"

	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"redis": []interface{}{
				map[string]interface{}{
					"instance_guid": "different-instance",
					"credentials":   map[string]interface{}{"uri": "redis://:pw@host:6379"},
				},
			},
		},
	}
	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err == nil {
		t.Fatalf("expected error when instance not in VCAP_SERVICES")
	}
}

func TestVCAPRecovery_Redis_NormalizesPasswordFromURI(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "redis-inst"
	appGUID := "app-guid-4"

	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"redis": []interface{}{
				map[string]interface{}{
					"instance_guid": instanceID,
					"credentials": map[string]interface{}{
						// only URI present; recovery should populate password
						"uri": "redis://:supersecret@redis.internal:6379",
					},
				},
			},
		},
	}

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	creds, _ := vault.Get(instanceID + "/credentials")
	if creds["password"] != "supersecret" {
		t.Errorf("expected password to be extracted from URI, got %v", creds["password"])
	}
}

func TestVCAPRecovery_PostgreSQL_NormalizesDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "pg-inst"
	appGUID := "app-guid-5"

	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"postgresql": []interface{}{
				map[string]interface{}{
					"instance_guid": instanceID,
					"credentials": map[string]interface{}{
						"username": "pguser",
						"password": "pgpass",
						"name":     "pgdb",
					},
				},
			},
		},
	}

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &MockTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	creds, _ := vault.Get(instanceID + "/credentials")
	if creds["database"] != "pgdb" {
		t.Errorf("expected database normalized from name, got %v", creds["database"])
	}
}

func TestVCAPRecovery_BatchMixedResults(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instExisting := "inst-existing"
	instSuccess := "inst-success"
	instFail := "inst-fail"
	appGUID := "app-guid-6"

	cfManager := newMockCFManager()
	cfManager.apps[instExisting] = []string{appGUID} // irrelevant, will be skipped
	cfManager.apps[instSuccess] = []string{appGUID}
	cfManager.apps[instFail] = []string{} // no apps -> fail

	cfManager.env[appGUID] = map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"rabbitmq": []interface{}{
				map[string]interface{}{
					"instance_guid": instSuccess,
					"credentials": map[string]interface{}{
						"username": "u",
						"password": "p",
						"protocols": map[string]interface{}{
							"amqp": map[string]interface{}{"host": "h", "port": 5672, "username": "u", "password": "p", "vhost": "/"},
						},
					},
				},
			},
		},
	}

	vault := newVCAPTestVault()
	// Pre-populate existing credentials to trigger skip
	_ = vault.Put(instExisting+"/credentials", map[string]interface{}{"ok": true})

	logger := &MockTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.BatchRecoverCredentials(ctx, []string{instExisting, instSuccess, instFail})
	if err == nil {
		t.Fatalf("expected batch error due to failures, got nil")
	}

	// Verify success stored
	_, err = vault.Get(instSuccess + "/credentials")
	if err != nil {
		t.Errorf("expected credentials stored for %s", instSuccess)
	}
	// Existing untouched
	_, err = vault.Get(instExisting + "/credentials")
	if err != nil {
		t.Errorf("expected existing credentials retained for %s", instExisting)
	}
	// Fail should not have creds
	_, err = vault.Get(instFail + "/credentials")
	if err == nil {
		t.Errorf("did not expect credentials for failing instance %s", instFail)
	}
}

func TestVCAPRecovery_MySQL_NormalizesDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "mysql-inst"
	appGUID := "app-guid-mysql"

	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"mysql": []interface{}{
				map[string]interface{}{
					"instance_guid": instanceID,
					"credentials": map[string]interface{}{
						"username": "muser",
						"password": "mpass",
						"name":     "mydb",
					},
				},
			},
		},
	}

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	creds, _ := vault.Get(instanceID + "/credentials")
	if creds["database"] != "mydb" {
		t.Errorf("expected database normalized from name, got %v", creds["database"])
	}

	if creds["username"] != "muser" || creds["password"] != "mpass" {
		t.Errorf("expected username/password preserved, got %v/%v", creds["username"], creds["password"])
	}
}

func TestVCAPRecovery_MySQL_NoName_NoDatabaseField(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "mysql-no-name"
	appGUID := "app-guid-mysql-2"

	vcap := map[string]interface{}{
		"vcap_services": map[string]interface{}{
			"mysql": []interface{}{
				map[string]interface{}{
					"instance_guid": instanceID,
					"credentials": map[string]interface{}{
						"username": "muser",
						"password": "mpass",
						// no name provided → no database normalization expected
					},
				},
			},
		},
	}

	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.env[appGUID] = vcap

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	creds, _ := vault.Get(instanceID + "/credentials")
	if _, ok := creds["database"]; ok {
		t.Errorf("did not expect database field when name is absent; got %v", creds["database"])
	}
}

func TestVCAPRecovery_CFAPIFindAppsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "cf-find-apps-error"
	cfManager := newMockCFManager()
	cfManager.findAppsErr[instanceID] = ErrCFAPIUnavailable

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err == nil {
		t.Fatalf("expected error from FindAppsByServiceInstance, got nil")
	}
}

func TestVCAPRecovery_CFAPIGetEnvError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "cf-get-env-error"
	appGUID := "app-guid-env-err"
	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	cfManager.getEnvErr[appGUID] = ErrCFEnvEndpointError

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err == nil {
		t.Fatalf("expected error from GetAppEnvironmentWithVCAP, got nil")
	}
}

func TestVCAPRecovery_InvalidVCAPType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	instanceID := "cf-invalid-vcap"
	appGUID := "app-guid-invalid-vcap"
	cfManager := newMockCFManager()
	cfManager.apps[instanceID] = []string{appGUID}
	// vcap_services present but of wrong type (string instead of map)
	cfManager.env[appGUID] = map[string]interface{}{"vcap_services": "not-a-map"}

	vault := newVCAPTestVault()
	logger := &CFTestLogger{}
	rec := NewCredentialVCAPRecovery(vault, cfManager, logger)

	err := rec.RecoverCredentialsFromVCAP(ctx, instanceID)
	if err == nil {
		t.Fatalf("expected error due to invalid vcap_services type")
	}
}

// Minimal logger for these tests; avoid name collision with other test loggers.
type CFTestLogger struct{ messages []string }

func (l *CFTestLogger) Debugf(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[DEBUG] "+format, args...))
}
func (l *CFTestLogger) Infof(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[INFO] "+format, args...))
}
func (l *CFTestLogger) Warningf(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[WARN] "+format, args...))
}
func (l *CFTestLogger) Errorf(format string, args ...interface{}) {
	l.messages = append(l.messages, fmt.Sprintf("[ERROR] "+format, args...))
}

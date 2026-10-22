package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/api"
	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade"
	pkgservices "blacksmith/pkg/services"
)

const (
	startupTaskID     = "5d0c1f7a-2b3e-4c5d-9e8f-0a1b2c3d4e5f"
	startupInstance   = "instance-7"
	startupDeployment = "redis-instance-7"
	startupLeaf       = "/ws-bosh/redis-instance-7/redis_crt"
	startupManifest   = "name: redis-instance-7\ninstance_groups: []\n"
	startupTasksPath  = "certificate-tasks"
	startupBOSHTask   = 913
)

// memoryVault keeps what the manager saves as JSON in memory. Any call it
// does not script panics through the nil embedded Vault.
type memoryVault struct {
	interfaces.Vault

	mu    sync.Mutex
	saved map[string][]byte
}

func (v *memoryVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	v.mu.Lock()
	raw, ok := v.saved[path]
	v.mu.Unlock()

	if !ok {
		return false, nil
	}

	return true, json.Unmarshal(raw, result)
}

func (v *memoryVault) Put(_ context.Context, path string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}

	v.mu.Lock()
	v.saved[path] = raw
	v.mu.Unlock()

	return nil
}

func (v *memoryVault) Delete(_ context.Context, path string) error {
	v.mu.Lock()
	delete(v.saved, path)
	v.mu.Unlock()

	return nil
}

// startupDirector is the director of the one deployment. Its deploy names
// a task that never finishes while the test runs.
type startupDirector struct {
	bosh.Director

	mu   sync.Mutex
	sent []bosh.DeployOptions
}

func (d *startupDirector) GetInfo(context.Context) (*bosh.Info, error) {
	return &bosh.Info{Name: "ws-bosh", Version: "280.0.4"}, nil
}

func (d *startupDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	return &bosh.DeploymentDetail{Name: name, Manifest: startupManifest}, nil
}

func (d *startupDirector) ListVariables(context.Context, string) ([]bosh.Variable, error) {
	return []bosh.Variable{{ID: "v1", Name: startupLeaf}}, nil
}

func (d *startupDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	return nil, nil
}

func (d *startupDirector) FindRunningTasksForDeployment(context.Context, string) ([]bosh.Task, error) {
	return nil, nil
}

func (d *startupDirector) FindRunningTaskForDeployment(context.Context, string) (*bosh.Task, error) {
	return nil, nil //nolint:nilnil // The real client answers no running task with nil.
}

func (d *startupDirector) UpdateDeploymentWithOptions(_ context.Context, name, _ string, opts bosh.DeployOptions) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.sent = append(d.sent, opts)

	return &bosh.Task{ID: startupBOSHTask, State: "processing", Deployment: name}, nil
}

func (d *startupDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	<-ctx.Done()

	return nil, fmt.Errorf("waiting for task %d: %w", taskID, ctx.Err())
}

func (d *startupDirector) CancelTask(context.Context, int) error { return nil }

func (d *startupDirector) deploys() []bosh.DeployOptions {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]bosh.DeployOptions(nil), d.sent...)
}

// staticTokens hands the CredHub client a fixed token.
type staticTokens struct{}

func (staticTokens) Token(context.Context) (string, error) { return "test-token", nil }
func (staticTokens) Invalidate(string)                     {}

// startupPKI is a throwaway CA with the old and new leaf it signed.
type startupPKI struct {
	caPEM, oldPEM, newPEM string
}

func newStartupPKI(t *testing.T) startupPKI {
	t.Helper()

	now := time.Now()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating the CA key: %v", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "startup-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating the CA: %v", err)
	}

	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing the CA: %v", err)
	}

	leaf := func(serial int64, notBefore, notAfter time.Time) string {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generating a leaf key: %v", err)
		}

		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: "redis.service"},
			DNSNames:     []string{"redis.service"},
			NotBefore:    notBefore,
			NotAfter:     notAfter,
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}

		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("creating a leaf: %v", err)
		}

		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}

	return startupPKI{
		caPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		oldPEM: leaf(2, now.Add(-time.Hour), now.Add(30*24*time.Hour)),
		newPEM: leaf(3, now.Add(-time.Minute), now.Add(365*24*time.Hour)),
	}
}

// startCredHub serves the leaf's metadata, its versions newest first, and
// each version's certificate and CA.
func startCredHub(t *testing.T, pki startupPKI) *credhub.Client {
	t.Helper()

	created := time.Now().Add(-time.Minute)
	pems := map[string]string{"v1": pki.oldPEM, "v2": pki.newPEM}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/certificates", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"certificates": []map[string]interface{}{{
				"id": "cert-7", "name": startupLeaf, "signed_by": "",
				"versions": []map[string]interface{}{
					{"id": "v2", "expiry_date": time.Now().Add(365 * 24 * time.Hour)},
					{"id": "v1", "expiry_date": time.Now().Add(30 * 24 * time.Hour)},
				},
			}},
		})
	})
	mux.HandleFunc("/api/v1/data", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"id": "v2", "type": "certificate", "version_created_at": created},
				{"id": "v1", "type": "certificate", "version_created_at": created.Add(-30 * 24 * time.Hour)},
			},
		})
	})
	mux.HandleFunc("/api/v1/data/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/data/")

		certificate, ok := pems[id]
		if !ok {
			http.NotFound(w, r)

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": id, "name": startupLeaf, "version_created_at": created,
			"value": map[string]string{"certificate": certificate, "ca": pki.caPEM},
		})
	})

	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))

	client, err := credhub.NewClient(credhub.ClientConfig{URL: server.URL, CACert: caPEM}, staticTokens{}, nil)
	if err != nil {
		t.Fatalf("building the CredHub client: %v", err)
	}

	return client
}

// startupTask is a confirmed redeploy task stored while its one instance
// was past the regeneration of v2.
func startupTask() upgrade.UpgradeTask {
	created := time.Now().Add(-time.Hour)
	started := created
	digest := sha256.Sum256([]byte(startupManifest))

	return upgrade.UpgradeTask{
		ID:                startupTaskID,
		Name:              "rotate the redis leaf",
		Kind:              upgrade.TaskKindCertificates,
		Status:            upgrade.TaskStatusRunning,
		CreationConfirmed: true,
		Certificates:      &upgrade.CertificateTaskSpec{Mode: upgrade.RotationModeRedeploy, PreviewID: "pv-startup"},
		TotalCount:        1,
		CreatedAt:         created,
		StartedAt:         &started,
		Instances: []upgrade.InstanceUpgrade{{
			InstanceID:     startupInstance,
			DeploymentName: startupDeployment,
			Status:         upgrade.InstanceStatusRunning,
			StartedAt:      &started,
			Certificates: &upgrade.InstanceRotation{
				Phase: upgrade.RotationPhaseRegenerate,
				Approved: upgrade.ApprovedEffects{
					ManifestDigest: "sha256:" + hex.EncodeToString(digest[:]),
					Selected:       []upgrade.VersionPair{{Name: startupLeaf, DeployedVersionID: "v1", LatestVersionID: "v1"}},
				},
				Variables: []upgrade.VariableRotation{{
					Name: startupLeaf, CertificateID: "cert-7",
					OldVersionID: "v1", NewVersionID: "v2",
					RegenerateState: upgrade.RegenerateConfirmed,
				}},
			},
		}},
	}
}

// A broker that restarts with a confirmed certificate task in Vault builds
// its manager with every dependency before the load, so the task goes on
// to its deploy, and the manager probes with the inventory's timeout.
func TestAPIStartupResumesPersistedCertificateTask(t *testing.T) {
	t.Parallel()

	task := startupTask()

	record, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("encoding the task: %v", err)
	}

	index, err := json.Marshal(map[string]interface{}{
		startupTaskID: map[string]interface{}{"status": task.Status, "created_at": task.CreatedAt.Unix()},
	})
	if err != nil {
		t.Fatalf("encoding the index: %v", err)
	}

	vault := &memoryVault{saved: map[string][]byte{
		startupTasksPath:                       index,
		startupTasksPath + "/" + startupTaskID: record,
	}}

	director := &startupDirector{}
	enabled := true

	deps := api.Dependencies{
		Config:             &mockConfig{},
		Logger:             &mockLogger{},
		Vault:              vault,
		Broker:             &mockBroker{},
		Director:           director,
		CFManager:          &mockCFManager{},
		ServicesManager:    &pkgservices.Manager{},
		SecurityMiddleware: &pkgservices.SecurityMiddleware{},
		CredHub:            startCredHub(t, newStartupPKI(t)),
		Switches: func() config.CertificatesConfig {
			return config.CertificatesConfig{Rotation: config.CertificateSwitch{Enabled: &enabled}}
		},
	}

	apiHandler := api.NewInternalAPI(deps)
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	manager := apiHandler.UpgradeHandler().GetManager()

	select {
	case <-manager.Loaded():
	case <-time.After(5 * time.Second):
		t.Fatal("the upgrade manager did not load its stored tasks")
	}

	if got := manager.Dependencies().Prober.Timeout; got != 5*time.Second {
		t.Fatalf("the manager probes with a %s timeout, want 5s", got)
	}

	deadline := time.Now().Add(10 * time.Second)
	for len(director.deploys()) == 0 {
		if time.Now().After(deadline) {
			stored, _ := manager.GetTask(startupTaskID)
			t.Fatalf("the loaded task never reached its deploy: %+v", stored)
		}

		time.Sleep(20 * time.Millisecond)
	}

	if opts := director.deploys()[0]; !opts.ForceLatestVariables {
		t.Fatalf("the deploy was sent with %+v, want ForceLatestVariables", opts)
	}
}

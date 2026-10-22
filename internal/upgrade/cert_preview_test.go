package upgrade

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
)

const (
	previewDirectorName = "ws-bosh"
	previewInstance2    = "instance-2"
	previewDeployment2  = "redis-instance-2"
	previewGone         = "instance-gone"
)

// previewLeaf, previewCA, and previewPassword name the variables of a test
// deployment in CredHub.
func previewLeaf(deployment string) string {
	return "/" + previewDirectorName + "/" + deployment + "/redis_crt"
}

func previewCA(deployment string) string {
	return "/" + previewDirectorName + "/" + deployment + "/services_ca"
}

func previewPassword(deployment string) string {
	return "/" + previewDirectorName + "/" + deployment + "/redis_password"
}

func previewManifest(deployment string) string {
	return "name: " + deployment + "\ninstance_groups:\n- name: redis\n  instances: 3\n"
}

// previewDirector answers the director reads a preview makes. A deployment
// with no manifest answers the director's 404. A call it does not script
// panics through the nil embedded Director.
type previewDirector struct {
	bosh.Director

	mu        sync.Mutex
	version   string
	manifests map[string]string
	variables map[string][]bosh.Variable
	running   map[string][]bosh.Task
}

func (d *previewDirector) GetInfo(context.Context) (*bosh.Info, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return &bosh.Info{Name: previewDirectorName, Version: d.version}, nil
}

func (d *previewDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	manifest, ok := d.manifests[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
	}

	return &bosh.DeploymentDetail{Name: name, Manifest: manifest}, nil
}

func (d *previewDirector) ListVariables(_ context.Context, deployment string) ([]bosh.Variable, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]bosh.Variable(nil), d.variables[deployment]...), nil
}

func (d *previewDirector) FindRunningTasksForDeployment(_ context.Context, deployment string) ([]bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]bosh.Task(nil), d.running[deployment]...), nil
}

// previewTokens hands the CredHub client a fixed token.
type previewTokens struct{}

func (previewTokens) Token(context.Context) (string, error) { return "preview-token", nil }

func (previewTokens) Invalidate(string) {}

// previewCredHub is a TLS CredHub fake that answers the certificate
// metadata and version reads the production client makes.
type previewCredHub struct {
	server *httptest.Server

	mu sync.Mutex
	// certs maps a name to the JSON of its entry in the certificates answer.
	certs map[string]string
	// latest maps a name to the ID of its newest version.
	latest map[string]string
	// failVersions maps a name to the status its versions read answers.
	failVersions map[string]int
}

func newPreviewCredHub(t *testing.T) *previewCredHub {
	t.Helper()

	fake := &previewCredHub{
		certs:        make(map[string]string),
		latest:       make(map[string]string),
		failVersions: make(map[string]int),
	}
	fake.server = httptest.NewTLSServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *previewCredHub) handle(writer http.ResponseWriter, request *http.Request) {
	name := request.URL.Query().Get("name")

	f.mu.Lock()
	defer f.mu.Unlock()

	writer.Header().Set("Content-Type", "application/json")

	switch request.URL.Path {
	case "/api/v1/certificates":
		entry, ok := f.certs[name]
		if !ok {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = fmt.Fprintf(writer, `{"certificates":[%s]}`, entry)
	case "/api/v1/data":
		if status, failing := f.failVersions[name]; failing {
			writer.WriteHeader(status)

			return
		}

		id, ok := f.latest[name]
		if !ok {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = fmt.Fprintf(writer, `{"data":[{"id":%q,"type":"password","version_created_at":"2026-10-20T00:00:00Z","value":"a-value-the-client-discards"}]}`, id)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

// certificate records a certificate whose one version is versionID.
func (f *previewCredHub) certificate(name, certificateID, versionID string, isCA bool, expiry time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.certs[name] = fmt.Sprintf(`{"id":%q,"name":%q,"signed_by":"","versions":[{"id":%q,"expiry_date":%q,"certificate_authority":%t}]}`,
		certificateID, name, versionID, expiry.Format(time.RFC3339), isCA)
}

func (f *previewCredHub) setLatest(name, versionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.latest[name] = versionID
}

func (f *previewCredHub) failVersionsRead(name string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failVersions[name] = status
}

func (f *previewCredHub) client(t *testing.T) *credhub.Client {
	t.Helper()

	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}))

	client, err := credhub.NewClient(credhub.ClientConfig{URL: f.server.URL, CACert: ca}, previewTokens{}, nil)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

// previewEnv is a manager with a director, a CredHub, and scans of two
// instances, instance-1 with 3 VMs and instance-2 with 1 VM.
type previewEnv struct {
	manager  *Manager
	director *previewDirector
	credhub  *previewCredHub
	ca       testCA

	mu       sync.Mutex
	deps     Deps
	scans    map[string]certinventory.InstanceScan
	switches config.CertificatesConfig
}

func newPreviewEnv(t *testing.T) *previewEnv {
	t.Helper()

	env := &previewEnv{
		director: &previewDirector{
			version:   "280.0.4",
			manifests: make(map[string]string),
			variables: make(map[string][]bosh.Variable),
			running:   make(map[string][]bosh.Task),
		},
		credhub:  newPreviewCredHub(t),
		ca:       newTestCA(t, "services-ca"),
		scans:    make(map[string]certinventory.InstanceScan),
		switches: switches(true, true),
	}

	env.addInstance(t, waitInstanceID, waitDeployment, "vm-c", "vm-a", "vm-b")
	env.addInstance(t, previewInstance2, previewDeployment2, "vm-z")

	env.manager = NewManager(&logger.NoOpLogger{}, env.director, newWaitVault())
	t.Cleanup(env.manager.Stop)

	env.deps = Deps{
		CredHub:      env.credhub.client(t),
		CredHubState: func() (credhub.CapabilityState, string) { return credhub.StateConfigured, "" },
		ScanInstance: env.scan,
		Switches:     env.currentSwitches,
		Settings:     certinventory.DefaultSettings,
		Now:          func() time.Time { return validateNow },
	}
	env.configure(t, nil)

	return env
}

// configure applies the environment's dependencies after change edits them.
func (e *previewEnv) configure(t *testing.T, change func(*Deps)) {
	t.Helper()

	deps := e.deps
	if change != nil {
		change(&deps)
	}

	err := e.manager.Configure(deps)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
}

func (e *previewEnv) currentSwitches() config.CertificatesConfig {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.switches
}

func (e *previewEnv) setSwitches(rotation, patch bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.switches = switches(rotation, patch)
}

func (e *previewEnv) scan(_ context.Context, instanceID string) (certinventory.InstanceScan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	scan, ok := e.scans[instanceID]
	if !ok {
		return certinventory.InstanceScan{}, certinventory.ErrUnknownInstance
	}

	return scan, nil
}

// addInstance gives the instance a deployment with a leaf at v1, the
// services CA at ca1, and a password at p1, each also the latest version.
func (e *previewEnv) addInstance(t *testing.T, instanceID, deployment string, cids ...string) {
	t.Helper()

	leaf, _ := e.ca.issue(t, baseSpec())
	expiry := validateNow.Add(5 * 24 * time.Hour)

	e.director.manifests[deployment] = previewManifest(deployment)
	e.director.variables[deployment] = []bosh.Variable{
		{ID: "v1", Name: previewLeaf(deployment)},
		{ID: "ca1", Name: previewCA(deployment)},
		{ID: "p1", Name: previewPassword(deployment)},
	}

	e.credhub.certificate(previewLeaf(deployment), "cert-"+deployment, "v1", false, expiry)
	e.credhub.certificate(previewCA(deployment), "ca-"+deployment, "ca1", true, expiry)
	e.credhub.setLatest(previewLeaf(deployment), "v1")
	e.credhub.setLatest(previewCA(deployment), "ca1")
	e.credhub.setLatest(previewPassword(deployment), "p1")

	vms := make([]bosh.VM, 0, len(cids))
	for _, cid := range cids {
		vms = append(vms, bosh.VM{CID: cid, Job: "redis"})
	}

	e.scans[instanceID] = certinventory.InstanceScan{
		Ref: certinventory.InstanceRef{
			ID: instanceID, Name: "orders-cache", Deployment: deployment,
			ServiceID: "redis-id", PlanID: "cluster-7-id", ServiceName: "redis", PlanName: "cluster-7",
		},
		Material: certinventory.ScanMaterial{
			CredHub: []certinventory.CredHubCert{
				{
					Name: previewLeaf(deployment),
					Versions: certinventory.Versions{
						CertificateID: "cert-" + deployment, DeployedVersionID: "v1", LatestVersionID: "v1",
						SigningCA: previewCA(deployment),
					},
					Deployed: leaf,
				},
				{
					Name:     previewCA(deployment),
					Versions: certinventory.Versions{CertificateID: "ca-" + deployment, DeployedVersionID: "ca1", LatestVersionID: "ca1"},
					Deployed: e.ca.cert,
				},
			},
			SigningCAs: map[string]*x509.Certificate{previewCA(deployment): e.ca.cert},
		},
		VMs: vms,
	}
}

// redeploy asks for a redeploy of the leaf of each instance.
func redeploy(instanceIDs ...string) PreviewRequest {
	req := PreviewRequest{Mode: RotationModeRedeploy}

	for _, id := range instanceIDs {
		deployment := "redis-" + id
		req.Selections = append(req.Selections, Selection{InstanceID: id, Variables: []string{previewLeaf(deployment)}})
	}

	return req
}

// expectAPIError fails the test unless err is an *apierr.Error with the
// status and code.
func expectAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an API error %d %s, got %v", status, code, err)
	}

	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("expected %d %s, got %d %s (%s)", status, code, apiErr.Status, apiErr.Code, apiErr.Message)
	}
}

func previewOf(t *testing.T, env *previewEnv, req PreviewRequest) *Preview {
	t.Helper()

	preview, err := env.manager.PreviewRotation(context.Background(), req)
	if err != nil {
		t.Fatalf("PreviewRotation: %v", err)
	}

	return preview
}

func TestPreviewBindsDigestVersionsAndCIDs(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	if !strings.HasPrefix(preview.ID, "pv-") || !preview.ExpiresAt.Equal(validateNow.Add(15*time.Minute)) {
		t.Fatalf("expected a pv- ID expiring in 15 minutes, got %q at %s", preview.ID, preview.ExpiresAt)
	}

	if len(preview.Instances) != 1 {
		t.Fatalf("expected one instance, got %+v", preview.Instances)
	}

	inst := preview.Instances[0]
	if inst.Blocked != nil {
		t.Fatalf("the instance is blocked: %+v", inst.Blocked)
	}

	sum := sha256.Sum256([]byte(previewManifest(waitDeployment)))
	if want := "sha256:" + hex.EncodeToString(sum[:]); inst.ManifestDigest != want || inst.approved.ManifestDigest != want {
		t.Fatalf("expected digest %s, got %s and bound %s", want, inst.ManifestDigest, inst.approved.ManifestDigest)
	}

	want := PreviewVariable{Name: previewLeaf(waitDeployment), DeployedVersionID: "v1", LatestVersionID: "v1", DaysLeft: 5}
	if len(inst.Selected) != 1 || inst.Selected[0] != want {
		t.Fatalf("expected selected %+v, got %+v", want, inst.Selected)
	}

	pair := VersionPair{Name: want.Name, DeployedVersionID: "v1", LatestVersionID: "v1"}
	if len(inst.approved.Selected) != 1 || inst.approved.Selected[0] != pair {
		t.Fatalf("expected the bound selection %+v, got %+v", pair, inst.approved.Selected)
	}

	if cids := inst.approved.VMCIDs; len(cids) != 3 || !sort.StringsAreSorted(cids) {
		t.Fatalf("expected three sorted VM CIDs, got %v", cids)
	}

	if inst.VMs != 3 || inst.InstanceName != "orders-cache" || inst.Service != "redis" || inst.Plan != "cluster-7" || inst.Mode != RotationModeRedeploy {
		t.Fatalf("the instance is described wrongly: %+v", inst)
	}

	if inst.certificateIDs[want.Name] != "cert-"+waitDeployment || inst.deployment != waitDeployment {
		t.Fatalf("expected the certificate ID and deployment bound, got %v and %q", inst.certificateIDs, inst.deployment)
	}

	if !strings.Contains(inst.Impact, "3 VMs") || !strings.Contains(inst.Impact, "canaries") || !strings.Contains(inst.Impact, "update block") {
		t.Fatalf("the impact sentence does not describe the roll: %q", inst.Impact)
	}

	if len(inst.Additional) != 0 || len(preview.Warnings) != 0 {
		t.Fatalf("expected no additional variables and no warnings, got %+v and %v", inst.Additional, preview.Warnings)
	}

	stored, err := env.manager.certDeps().Previews.Get(preview.ID)
	if err != nil || stored != preview {
		t.Fatalf("expected the preview to be stored, got %v and %v", stored, err)
	}
}

func TestPreviewListsAdditionalPassword(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)
	env.credhub.setLatest(previewPassword(waitDeployment), "p2")

	inst := previewOf(t, env, redeploy(waitInstanceID)).Instances[0]

	want := VersionPair{Name: previewPassword(waitDeployment), DeployedVersionID: "p1", LatestVersionID: "p2"}
	if len(inst.Additional) != 1 || inst.Additional[0] != want {
		t.Fatalf("expected additional %+v, got %+v", want, inst.Additional)
	}

	if len(inst.approved.Additional) != 1 || inst.approved.Additional[0] != want {
		t.Fatalf("expected the bound additional %+v, got %+v", want, inst.approved.Additional)
	}
}

func TestPreviewCredHubFailureBlocksPreview(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)
	env.credhub.failVersionsRead(previewPassword(waitDeployment), http.StatusServiceUnavailable)

	_, err := env.manager.PreviewRotation(context.Background(), redeploy(waitInstanceID))
	expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable)
}

func TestPreviewBlocksBusyInstance(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)

	release, err := env.manager.Guard().Acquire(waitInstanceID, waitDeployment, mutation.Holder{
		Kind: mutation.KindUpgrade, TaskID: "upgrade-9", Description: `upgrade "nightly stemcells"`,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)

	preview := previewOf(t, env, redeploy(waitInstanceID, previewInstance2))

	busy := preview.Instances[0].Blocked
	if busy == nil || busy.Code != apierr.CodeInstanceBusy || !strings.Contains(busy.Message, `upgrade "nightly stemcells"`) {
		t.Fatalf("expected instance_busy naming the upgrade, got %+v", busy)
	}

	if other := preview.Instances[1]; other.Blocked != nil || len(other.Selected) != 1 {
		t.Fatalf("the other instance must be clear, got %+v", other)
	}
}

func TestPreviewInstanceAllowsMatchingHolder(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)

	release, err := env.manager.Guard().Acquire(waitInstanceID, waitDeployment, mutation.Holder{
		Kind: mutation.KindCertificates, TaskID: "task-T", Description: `certificate rotation "CHG-1"`,
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)

	req := redeploy(waitInstanceID)

	own := env.manager.previewInstance(context.Background(), req, req.Selections[0], "task-T")
	if own.failure != nil || own.Blocked != nil {
		t.Fatalf("the holder's own preview must not be blocked, got %+v and %v", own.Blocked, own.failure)
	}

	other := env.manager.previewInstance(context.Background(), req, req.Selections[0], "")
	if other.Blocked == nil || other.Blocked.Code != apierr.CodeInstanceBusy {
		t.Fatalf("an ordinary preview must be blocked, got %+v", other.Blocked)
	}
}

func TestPreviewBlocksDeprovisionedInstance(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)

	env.director.mu.Lock()
	delete(env.director.manifests, previewDeployment2)
	env.director.mu.Unlock()

	req := redeploy(previewGone, previewInstance2, waitInstanceID)

	preview, err := env.manager.PreviewRotation(context.Background(), req)
	if err != nil {
		t.Fatalf("a deleted instance must not fail the preview: %v", err)
	}

	for i, id := range []string{previewGone, previewInstance2} {
		blocked := preview.Instances[i].Blocked
		if blocked == nil || blocked.Code != apierr.CodeInstanceNotFound {
			t.Fatalf("expected %s blocked with instance_not_found, got %+v", id, blocked)
		}
	}

	if remaining := preview.Instances[2]; remaining.Blocked != nil {
		t.Fatalf("the remaining instance must be clear, got %+v", remaining.Blocked)
	}
}

func TestPreviewRejectsCAAndForeignNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		variable string
		code     string
	}{
		{"the services CA", previewCA(waitDeployment), apierr.CodeNotRotatable},
		{"a name under another deployment", previewLeaf(previewDeployment2), apierr.CodeNotRotatable},
		{"a name under another director", "/other-bosh/" + waitDeployment + "/redis_crt", apierr.CodeNotRotatable},
		{"a name not in the deployment", "/" + previewDirectorName + "/" + waitDeployment + "/missing_crt", apierr.CodeUnknownVariable},
		{"a variable that is not a certificate", previewPassword(waitDeployment), apierr.CodeNotRotatable},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newPreviewEnv(t)
			req := PreviewRequest{Mode: RotationModeRedeploy, Selections: []Selection{{InstanceID: waitInstanceID, Variables: []string{tc.variable}}}}

			blocked := previewOf(t, env, req).Instances[0].Blocked
			if blocked == nil || blocked.Code != tc.code {
				t.Fatalf("expected %s, got %+v", tc.code, blocked)
			}
		})
	}
}

func TestPreviewBlocksChangedSigningCA(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)
	other := newTestCA(t, "another-ca")

	env.mu.Lock()
	scan := env.scans[waitInstanceID]
	scan.Material.SigningCAs = map[string]*x509.Certificate{previewCA(waitDeployment): other.cert}
	env.scans[waitInstanceID] = scan
	env.mu.Unlock()

	blocked := previewOf(t, env, redeploy(waitInstanceID)).Instances[0].Blocked
	if blocked == nil || blocked.Code != apierr.CodeSigningCAChanged {
		t.Fatalf("expected signing_ca_changed, got %+v", blocked)
	}
}

func TestPreviewBlocksLockedDeployment(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)

	env.director.mu.Lock()
	env.director.running[waitDeployment] = []bosh.Task{{ID: 77, State: "processing", Deployment: waitDeployment}}
	env.director.mu.Unlock()

	blocked := previewOf(t, env, redeploy(waitInstanceID)).Instances[0].Blocked
	if blocked == nil || blocked.Code != apierr.CodeDeploymentLocked || !strings.Contains(blocked.Message, "77") {
		t.Fatalf("expected deployment_locked naming task 77, got %+v", blocked)
	}
}

func TestPreviewPatchBindsGateAndWarns(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)

	req := redeploy(previewInstance2)
	req.Mode = RotationModePatch
	req.Converge = ConvergePending

	preview := previewOf(t, env, req)

	inst := preview.Instances[0]
	if inst.Blocked != nil || inst.Gate != "single-vm" || inst.Mode != RotationModePatch {
		t.Fatalf("expected a clear single-vm patch, got %+v", inst)
	}

	if len(preview.Warnings) != 1 || !strings.HasPrefix(preview.Warnings[0], "Temporary repair") {
		t.Fatalf("expected the temporary repair warning, got %v", preview.Warnings)
	}
}

func TestPreviewConvergeListsEveryChangedVariable(t *testing.T) {
	t.Parallel()

	env := newPreviewEnv(t)
	env.credhub.setLatest(previewLeaf(waitDeployment), "v2")
	env.credhub.setLatest(previewPassword(waitDeployment), "p2")

	req := PreviewRequest{Mode: RotationModeConverge, Selections: []Selection{{InstanceID: waitInstanceID}}}

	inst := previewOf(t, env, req).Instances[0]
	if inst.Blocked != nil || len(inst.Selected) != 0 {
		t.Fatalf("expected a clear converge with nothing selected, got %+v", inst)
	}

	if len(inst.Additional) != 2 || inst.Additional[0].Name != previewLeaf(waitDeployment) || inst.Additional[1].Name != previewPassword(waitDeployment) {
		t.Fatalf("expected the leaf and the password in name order, got %+v", inst.Additional)
	}
}

func TestPreviewRequestErrors(t *testing.T) {
	t.Parallel()

	leaf := previewLeaf(waitDeployment)
	selection := []Selection{{InstanceID: waitInstanceID, Variables: []string{leaf}}}

	tests := []struct {
		name   string
		req    PreviewRequest
		change func(env *previewEnv, deps *Deps)
		status int
		code   string
	}{
		{name: "no selections", req: PreviewRequest{Mode: RotationModeRedeploy}, status: http.StatusBadRequest, code: apierr.CodeNoSelections},
		{
			name:   "a selection with no variables",
			req:    PreviewRequest{Mode: RotationModeRedeploy, Selections: []Selection{{InstanceID: waitInstanceID}}},
			status: http.StatusBadRequest, code: apierr.CodeNoSelections,
		},
		{name: "an unknown mode", req: PreviewRequest{Mode: "shuffle", Selections: selection}, status: http.StatusBadRequest, code: apierr.CodeInvalidMode},
		{name: "patch without a converge policy", req: PreviewRequest{Mode: RotationModePatch, Selections: selection}, status: http.StatusBadRequest, code: apierr.CodeInvalidMode},
		{
			name:   "a converge policy with redeploy",
			req:    PreviewRequest{Mode: RotationModeRedeploy, Converge: ConvergePending, Selections: selection},
			status: http.StatusBadRequest, code: apierr.CodeInvalidMode,
		},
		{
			name:   "rotation off",
			req:    PreviewRequest{Mode: RotationModeRedeploy, Selections: selection},
			change: func(env *previewEnv, _ *Deps) { env.setSwitches(false, true) },
			status: http.StatusForbidden, code: apierr.CodeRotationDisabled,
		},
		{
			name:   "patch off",
			req:    PreviewRequest{Mode: RotationModePatch, Converge: ConvergePending, Selections: selection},
			change: func(env *previewEnv, _ *Deps) { env.setSwitches(true, false) },
			status: http.StatusForbidden, code: apierr.CodePatchDisabled,
		},
		{
			name:   "no CredHub",
			req:    PreviewRequest{Mode: RotationModeRedeploy, Selections: selection},
			change: func(_ *previewEnv, deps *Deps) { deps.CredHub = nil },
			status: http.StatusForbidden, code: apierr.CodeCredHubNotConfigured,
		},
		{
			name: "CredHub unreachable",
			req:  PreviewRequest{Mode: RotationModeRedeploy, Selections: selection},
			change: func(_ *previewEnv, deps *Deps) {
				deps.CredHubState = func() (credhub.CapabilityState, string) { return credhub.StateUnreachable, "dial tcp: refused" }
			},
			status: http.StatusServiceUnavailable, code: apierr.CodeCredHubUnavailable,
		},
		{
			name: "an old director",
			req:  PreviewRequest{Mode: RotationModeRedeploy, Selections: selection},
			change: func(env *previewEnv, _ *Deps) {
				env.director.mu.Lock()
				env.director.version = "278.1.0"
				env.director.mu.Unlock()
			},
			status: http.StatusForbidden, code: apierr.CodeDirectorUnsupported,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newPreviewEnv(t)
			if tc.change != nil {
				env.configure(t, func(deps *Deps) { tc.change(env, deps) })
			}

			_, err := env.manager.PreviewRotation(context.Background(), tc.req)
			expectAPIError(t, err, tc.status, tc.code)
		})
	}
}

func TestPreviewStoreExpiry(t *testing.T) {
	t.Parallel()

	now := validateNow
	store := NewPreviewStore(func() time.Time { return now })

	first := &Preview{ID: "pv-first"}
	store.Put(first, 15*time.Minute)
	store.Put(&Preview{ID: "pv-never-read"}, 15*time.Minute)

	if !first.ExpiresAt.Equal(validateNow.Add(15 * time.Minute)) {
		t.Fatalf("expected the expiry 15 minutes on, got %s", first.ExpiresAt)
	}

	got, err := store.Get("pv-first")
	if err != nil || got != first {
		t.Fatalf("expected the stored preview, got %v and %v", got, err)
	}

	_, err = store.Get("pv-unknown")
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	now = validateNow.Add(16 * time.Minute)

	_, err = store.Get("pv-first")
	expectAPIError(t, err, http.StatusGone, apierr.CodePreviewExpired)

	store.Put(&Preview{ID: "pv-second"}, 15*time.Minute)

	if count := store.count(); count != 1 {
		t.Fatalf("expected Put to sweep the expired preview, got %d stored", count)
	}
}

// count returns how many previews the store holds, expired or not.
func (s *PreviewStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.previews)
}

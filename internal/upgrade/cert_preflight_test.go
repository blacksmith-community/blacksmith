package upgrade

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/credhub"
	"blacksmith/internal/mutation"
)

var errScanBroken = errors.New("the scan broke")

// preflightEnv is a preview environment with a certificate task made from
// a preview of the test instance's leaf. The task holds the instance.
type preflightEnv struct {
	*previewEnv

	task *UpgradeTask
}

// vmsDirector adds the VM list to the preview director. Each deployment
// lists the VMs the test sets, and none by default.
type vmsDirector struct {
	*previewDirector

	vms map[string][]bosh.VM
}

func (d *vmsDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]bosh.VM, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.note(ctx, "GetDeploymentVMs")

	return append([]bosh.VM(nil), d.vms[deployment]...), nil
}

func newPreflightEnv(t *testing.T, mode RotationMode) *preflightEnv {
	t.Helper()

	env := newPreviewEnv(t)

	req := redeploy(waitInstanceID)
	if mode == RotationModeConverge {
		req = PreviewRequest{Mode: RotationModeConverge, Selections: []Selection{{InstanceID: waitInstanceID}}}
	}

	preview := previewOf(t, env, req)

	task := env.manager.newCertificateTask(createRequest(preview.ID), "digest", preview)
	if len(task.Instances) != 1 {
		t.Fatalf("expected one instance in the task, got %+v", task.Instances)
	}

	err := env.manager.reserveCertificateTask(task)
	if err != nil {
		t.Fatalf("reserving the task: %v", err)
	}

	// The preview bound the CIDs vm-a, vm-b, and vm-c, which the director
	// lists in another order.
	env.manager.director = &vmsDirector{
		previewDirector: env.director,
		vms:             map[string][]bosh.VM{waitDeployment: {{CID: "vm-c"}, {CID: "vm-a"}, {CID: "vm-b"}}},
	}

	return &preflightEnv{previewEnv: env, task: task}
}

func (e *preflightEnv) inst() *InstanceUpgrade { return &e.task.Instances[0] }

func (e *preflightEnv) preflight() error {
	return e.manager.certPreflight(context.Background(), e.task, e.inst())
}

func (e *preflightEnv) recheck(at RecheckPoint) ([]string, error) {
	return e.manager.recheckEffects(context.Background(), e.task, e.inst(), at)
}

// holdForSomeoneElse frees the task's reservation and gives the instance
// to an update.
func (e *preflightEnv) holdForSomeoneElse(t *testing.T) {
	t.Helper()

	e.manager.mu.Lock()
	e.manager.releaseAllLocked(e.task.ID)
	e.manager.mu.Unlock()

	_, err := e.manager.Guard().Acquire(waitInstanceID, waitDeployment, mutation.Holder{Kind: mutation.KindUpdate, Description: "an update"})
	if err != nil {
		t.Fatalf("acquiring the instance for an update: %v", err)
	}
}

func (e *preflightEnv) editDirector(change func(d *previewDirector)) {
	e.director.mu.Lock()
	defer e.director.mu.Unlock()

	change(e.director)
}

// setSigningCA makes the scan read ca as the leaf's signing CA.
func (e *preflightEnv) setSigningCA(ca *x509.Certificate) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.scans[waitInstanceID].Material.SigningCAs[previewCA(waitDeployment)] = ca
}

func TestCertPreflightOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mode   RotationMode
		inject func(t *testing.T, env *preflightEnv)
		status int
		code   string
	}{
		{
			name:   "rotation turned off",
			inject: func(_ *testing.T, env *preflightEnv) { env.setSwitches(false, false) },
			status: http.StatusForbidden, code: apierr.CodeRotationDisabled,
		},
		{
			name: "patch turned off for a patch task",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.task.Certificates.Mode = RotationModePatch
				env.setSwitches(true, false)
			},
			status: http.StatusForbidden, code: apierr.CodePatchDisabled,
		},
		{
			name: "no CredHub",
			inject: func(t *testing.T, env *preflightEnv) {
				env.configure(t, func(d *Deps) { d.CredHub = nil })
			},
			status: http.StatusForbidden, code: apierr.CodeCredHubNotConfigured,
		},
		{
			name:   "the instance is held by another holder",
			inject: func(t *testing.T, env *preflightEnv) { env.holdForSomeoneElse(t) },
			status: http.StatusConflict, code: apierr.CodeInstanceBusy,
		},
		{
			name: "the instance is not held at all",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.manager.mu.Lock()
				env.manager.releaseAllLocked(env.task.ID)
				env.manager.mu.Unlock()
			},
			status: http.StatusConflict, code: apierr.CodeInstanceBusy,
		},
		{
			name: "the director is too old",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.editDirector(func(d *previewDirector) { d.version = "278.0.1" })
			},
			status: http.StatusForbidden, code: apierr.CodeDirectorUnsupported,
		},
		{
			name: "a director task is queued on the deployment",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.editDirector(func(d *previewDirector) {
					d.running[waitDeployment] = []bosh.Task{{ID: 7, State: "queued", Deployment: waitDeployment}}
				})
			},
			status: http.StatusConflict, code: apierr.CodeDeploymentLocked,
		},
		{
			name: "a director task is processing on the deployment",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.editDirector(func(d *previewDirector) {
					d.running[waitDeployment] = []bosh.Task{{ID: 8, State: "processing", Deployment: waitDeployment}}
				})
			},
			status: http.StatusConflict, code: apierr.CodeDeploymentLocked,
		},
		{
			name: "CredHub is unreachable",
			inject: func(t *testing.T, env *preflightEnv) {
				env.configure(t, func(d *Deps) {
					d.CredHubState = func() (credhub.CapabilityState, string) { return credhub.StateUnreachable, "dial timeout" }
				})
			},
			status: http.StatusServiceUnavailable, code: apierr.CodeCredHubUnavailable,
		},
		{
			name: "the variable is no longer in the deployment",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.editDirector(func(d *previewDirector) {
					d.variables[waitDeployment] = slices.DeleteFunc(d.variables[waitDeployment], func(v bosh.Variable) bool {
						return v.Name == previewLeaf(waitDeployment)
					})
				})
			},
			status: http.StatusBadRequest, code: apierr.CodeUnknownVariable,
		},
		{
			name: "the variable is now a CA",
			inject: func(_ *testing.T, env *preflightEnv) {
				env.credhub.certificate(previewLeaf(waitDeployment), "cert-"+waitDeployment, "v1", true, validateNow.AddDate(0, 0, 5))
			},
			status: http.StatusBadRequest, code: apierr.CodeNotRotatable,
		},
		{
			name: "the signing CA changed",
			inject: func(t *testing.T, env *preflightEnv) {
				env.setSigningCA(newTestCA(t, "other-ca").cert)
			},
			status: http.StatusConflict, code: apierr.CodeSigningCAChanged,
		},
		{
			name: "a converge skips the signing CA check",
			mode: RotationModeConverge,
			inject: func(t *testing.T, env *preflightEnv) {
				env.setSigningCA(newTestCA(t, "other-ca").cert)
			},
		},
		{
			name: "a converge skips the variable checks, which need a scan",
			mode: RotationModeConverge,
			inject: func(t *testing.T, env *preflightEnv) {
				env.configure(t, func(d *Deps) {
					d.ScanInstance = func(context.Context, string) (certinventory.InstanceScan, error) {
						return certinventory.InstanceScan{}, errScanBroken
					}
				})
			},
		},
		{
			name: "a converge still checks the director's tasks",
			mode: RotationModeConverge,
			inject: func(_ *testing.T, env *preflightEnv) {
				env.editDirector(func(d *previewDirector) {
					d.running[waitDeployment] = []bosh.Task{{ID: 9, State: "processing", Deployment: waitDeployment}}
				})
			},
			status: http.StatusConflict, code: apierr.CodeDeploymentLocked,
		},
		{
			name:   "everything is in order",
			inject: func(*testing.T, *preflightEnv) {},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mode := tc.mode
			if mode == "" {
				mode = RotationModeRedeploy
			}

			env := newPreflightEnv(t, mode)
			tc.inject(t, env)

			err := env.preflight()
			if tc.code == "" {
				if err != nil {
					t.Fatalf("expected the preflight to pass, got %v", err)
				}

				return
			}

			expectAPIError(t, err, tc.status, tc.code)
		})
	}
}

// The switches come before every other check, so a task whose switch is
// off and whose director is also too old reports the switch.
func TestCertPreflightSwitchesComeFirst(t *testing.T) {
	t.Parallel()

	env := newPreflightEnv(t, RotationModeRedeploy)
	env.setSwitches(false, false)
	env.holdForSomeoneElse(t)
	env.editDirector(func(d *previewDirector) { d.version = "270.0.0" })

	expectAPIError(t, env.preflight(), http.StatusForbidden, apierr.CodeRotationDisabled)
}

// The guard comes before the director, so an instance held elsewhere on
// an old director reports instance_busy.
func TestCertPreflightGuardBeforeDirector(t *testing.T) {
	t.Parallel()

	env := newPreflightEnv(t, RotationModeRedeploy)
	env.holdForSomeoneElse(t)
	env.editDirector(func(d *previewDirector) { d.version = "270.0.0" })

	expectAPIError(t, env.preflight(), http.StatusConflict, apierr.CodeInstanceBusy)
}

func TestRecheckEffects(t *testing.T) {
	t.Parallel()

	leaf := previewLeaf(waitDeployment)

	// regenerated records that the task made v2 of the leaf, which is now
	// CredHub's latest.
	regenerated := func(env *preflightEnv) {
		env.inst().Certificates.Variables[0].RegenerateState = RegenerateConfirmed
		env.inst().Certificates.Variables[0].NewVersionID = "v2"
		env.credhub.setLatest(leaf, "v2")
	}

	changeManifest := func(env *preflightEnv) {
		env.editDirector(func(d *previewDirector) { d.manifests[waitDeployment] += "update:\n  canaries: 2\n" })
	}

	tests := []struct {
		name  string
		at    RecheckPoint
		setup func(env *preflightEnv)
		want  []string
	}{
		{name: "nothing changed before regenerating", at: BeforeRegenerate, setup: func(*preflightEnv) {}},
		{name: "nothing changed before mutating", at: BeforeMutation, setup: regenerated},
		{name: "nothing changed before converging", at: BeforeConverge, setup: regenerated},
		{
			name: "the manifest changed before regenerating", at: BeforeRegenerate, setup: changeManifest,
			want: []string{"The manifest changed since the preview."},
		},
		{
			name: "the manifest changed before mutating", at: BeforeMutation,
			setup: func(env *preflightEnv) { regenerated(env); changeManifest(env) },
			want:  []string{"The manifest changed since the preview."},
		},
		{
			name: "the manifest changed before converging", at: BeforeConverge,
			setup: func(env *preflightEnv) { regenerated(env); changeManifest(env) },
			want:  []string{"The manifest changed since the preview."},
		},
		{
			name: "another variable gained a version before mutating", at: BeforeMutation,
			setup: func(env *preflightEnv) {
				regenerated(env)
				env.credhub.setLatest(previewPassword(waitDeployment), "p2")
			},
			want: []string{"redis_password has a newer version (p2) than the deployed one, which the preview did not list."},
		},
		{
			name: "the selected leaf has a version the task did not make", at: BeforeMutation,
			setup: func(env *preflightEnv) {
				env.inst().Certificates.Variables[0].NewVersionID = "v3"
				env.credhub.setLatest(leaf, "v4")
			},
			want: []string{"redis_crt has a newer version (v4) that this task did not create."},
		},
		{
			name: "the selected leaf has no new version before mutating", at: BeforeMutation,
			setup: func(*preflightEnv) {},
			want:  []string{"redis_crt has no version that this task made."},
		},
		{
			name: "the selected leaf gained a version before regenerating", at: BeforeRegenerate,
			setup: func(env *preflightEnv) { env.credhub.setLatest(leaf, "v4") },
			want:  []string{"redis_crt has a newer version (v4) than the preview listed (v1)."},
		},
		{
			name: "the VMs changed before regenerating", at: BeforeRegenerate,
			setup: func(env *preflightEnv) {
				env.inst().Certificates.Approved.VMCIDs = []string{"vm-a", "vm-b", "vm-old"}
			},
			want: []string{"The instance's VMs changed since the preview."},
		},
		{
			name: "another variable gained a version before converging", at: BeforeConverge,
			setup: func(env *preflightEnv) {
				regenerated(env)
				env.credhub.setLatest(previewPassword(waitDeployment), "p2")
			},
			want: []string{"redis_password has a newer version (p2) than the deployed one, which this task did not expect."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newPreflightEnv(t, RotationModeRedeploy)
			tc.setup(env)

			changes, err := env.recheck(tc.at)
			if err != nil {
				t.Fatalf("recheckEffects: %v", err)
			}

			if !slices.Equal(changes, tc.want) {
				t.Fatalf("expected changes %q, got %q", tc.want, changes)
			}
		})
	}
}

// Each director call the preflight and the re-checks make runs under its
// own deadline of at most certinventory.DirectorCallTimeout, so a slow
// director can't hold a task step for as long as the adapter's HTTP
// timeout allows.
func TestPreflightBoundsEachDirectorCall(t *testing.T) {
	t.Parallel()

	env := newPreflightEnv(t, RotationModeRedeploy)
	env.editDirector(func(d *previewDirector) { d.deadlines = nil })

	err := env.preflight()
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}

	for _, at := range []RecheckPoint{BeforeRegenerate, BeforeMutation, BeforeConverge} {
		_, err = env.recheck(at)
		if err != nil {
			t.Fatalf("recheckEffects at %d: %v", at, err)
		}
	}

	env.director.mu.Lock()
	defer env.director.mu.Unlock()

	for _, call := range []string{"GetInfo", "FindRunningTasksForDeployment", "ListVariables", "GetDeployment", "GetDeploymentVMs"} {
		lefts := env.director.deadlines[call]
		if len(lefts) == 0 {
			t.Fatalf("the preflight and re-checks never called %s", call)
		}

		for _, left := range lefts {
			if left <= 0 || left > certinventory.DirectorCallTimeout {
				t.Fatalf("%s ran with %v before its deadline, want a deadline within %v", call, left, certinventory.DirectorCallTimeout)
			}
		}
	}
}

// selectSecond makes the password a second selected variable of the
// instance, at state, with the latest version latest in CredHub.
func (e *preflightEnv) selectSecond(state, newVersionID, latest string) {
	password := previewPassword(waitDeployment)

	rotation := e.inst().Certificates
	rotation.Approved.Selected = append(rotation.Approved.Selected, VersionPair{Name: password, DeployedVersionID: "p1", LatestVersionID: "p1"})
	rotation.Approved.Additional = slices.DeleteFunc(rotation.Approved.Additional, func(p VersionPair) bool { return p.Name == password })
	rotation.Variables = append(rotation.Variables, VariableRotation{
		Name: password, CertificateID: "cert-password", OldVersionID: "p1", Baseline: []string{"p1"},
		RegenerateState: state, NewVersionID: newVersionID,
	})

	e.credhub.setLatest(password, latest)
}

// A task that resumes after it regenerated a leaf finds that leaf's own
// new version in CredHub, which is no outside change. A leaf whose
// regeneration is being reconciled is left to the regeneration step, so
// the re-check neither stops the task for re-approval nor touches the
// pending decision.
func TestRecheckBeforeRegenerateAcceptsOwnVersions(t *testing.T) {
	t.Parallel()

	leaf := previewLeaf(waitDeployment)

	// confirmedLeaf records that the task made v2 of the leaf, which is
	// CredHub's latest.
	confirmedLeaf := func(env *preflightEnv, state, latest string) {
		env.inst().Certificates.Variables[0].Baseline = []string{"v1"}
		env.inst().Certificates.Variables[0].RegenerateState = state
		env.inst().Certificates.Variables[0].NewVersionID = "v2"
		env.credhub.setLatest(leaf, latest)
	}

	reconciling := &PendingDecision{
		Kind: DecisionReconcileRegeneration, Variable: previewPassword(waitDeployment), Since: validateNow,
		Candidates: []Candidate{{VersionID: "p2", CreatedAt: validateNow, Valid: true}},
	}

	tests := []struct {
		name  string
		setup func(env *preflightEnv)
		want  []string
	}{
		{
			name: "a confirmed leaf at its own version, with a second leaf being reconciled",
			setup: func(env *preflightEnv) {
				confirmedLeaf(env, RegenerateConfirmed, "v2")
				env.selectSecond(RegenerateOutcomeUnknown, "", "p2")
				env.inst().Status = StatusAwaitingDecision
				env.inst().Certificates.Pending = reconciling.Clone()
			},
		},
		{
			name:  "an adopted leaf at the version an operator adopted",
			setup: func(env *preflightEnv) { confirmedLeaf(env, RegenerateAdopted, "v2") },
		},
		{
			name: "a leaf that was sending when the broker stopped",
			setup: func(env *preflightEnv) {
				env.selectSecond(RegenerateSending, "", "p2")
			},
		},
		{
			name: "a second leaf not yet sent still gets compared",
			setup: func(env *preflightEnv) {
				confirmedLeaf(env, RegenerateConfirmed, "v2")
				env.selectSecond(RegenerateNotSent, "", "p2")
			},
			want: []string{"redis_password has a newer version (p2) than the preview listed (p1)."},
		},
		{
			name:  "a confirmed leaf whose latest version is not its own",
			setup: func(env *preflightEnv) { confirmedLeaf(env, RegenerateConfirmed, "v3") },
			want:  []string{"redis_crt has a newer version (v3) that this task did not create."},
		},
		{
			name: "a confirmed leaf that is deployed at another version",
			setup: func(env *preflightEnv) {
				confirmedLeaf(env, RegenerateConfirmed, "v2")
				env.editDirector(func(d *previewDirector) {
					for i := range d.variables[waitDeployment] {
						if d.variables[waitDeployment][i].Name == leaf {
							d.variables[waitDeployment][i].ID = "v0"
						}
					}
				})
			},
			want: []string{"redis_crt is deployed at version v0, not v1 as in the preview."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			env := newPreflightEnv(t, RotationModeRedeploy)
			tc.setup(env)

			before := env.inst().Certificates.Pending.Clone()

			changes, err := env.recheck(BeforeRegenerate)
			if err != nil {
				t.Fatalf("recheckEffects: %v", err)
			}

			if !slices.Equal(changes, tc.want) {
				t.Fatalf("expected changes %q, got %q", tc.want, changes)
			}

			if after := env.inst().Certificates.Pending; !reflect.DeepEqual(after, before) {
				t.Fatalf("expected the pending decision untouched, got %+v, was %+v", after, before)
			}
		})
	}
}

// A CredHub read of a selected certificate that fails during the
// preflight says the read was for the preflight, not for a preview.
func TestPreflightCredHubFailureNamesPreflight(t *testing.T) {
	t.Parallel()

	env := newPreflightEnv(t, RotationModeRedeploy)
	env.credhub.failCertificatesRead(previewLeaf(waitDeployment), http.StatusServiceUnavailable)

	err := env.preflight()
	expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable)

	if !strings.Contains(err.Error(), "the preflight check of the selected certificates needs") {
		t.Fatalf("expected the error to say the read was for the preflight, got %v", err)
	}
}

// A CredHub failure is an error, never an empty list of changes, at every
// point.
func TestRecheckEffectsCredHubUnavailable(t *testing.T) {
	t.Parallel()

	for _, at := range []RecheckPoint{BeforeRegenerate, BeforeMutation, BeforeConverge} {
		env := newPreflightEnv(t, RotationModeRedeploy)
		env.credhub.failVersionsRead(previewPassword(waitDeployment), http.StatusServiceUnavailable)

		changes, err := env.recheck(at)
		if changes != nil {
			t.Fatalf("point %d: expected no change list with the error, got %q", at, changes)
		}

		expectAPIError(t, err, http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable)

		if !strings.Contains(err.Error(), "CredHub") || !strings.Contains(err.Error(), "the re-check of the approved effects needs") {
			t.Fatalf("point %d: expected the error to name CredHub and the re-check, got %v", at, err)
		}
	}
}

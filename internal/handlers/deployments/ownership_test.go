package deployments_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// redisService is the service and the plan of the indexed instance.
const redisService = "redis"

// callRestart is the director method a restart ends in.
const callRestart = "RestartDeployment"

// managedVault indexes one service instance, whose deployment is
// guardedDeployment, and nothing else.
func managedVault() *testutil.InstanceVault {
	return managingVault(guardedDeployment)
}

// managingVault indexes one service instance whose deployment is the
// named one, so the broker manages that deployment and no other.
func managingVault(deployment string) *testutil.InstanceVault {
	return &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		ownedInstance: {ID: ownedInstance, ServiceID: redisService, PlanID: redisService, DeploymentName: deployment},
	}}
}

func ownershipHandler(vault *testutil.InstanceVault) (*deployments.Handler, *recordingDirector) {
	guard := mutation.NewGuard(time.Now)
	director := newRecordingDirector(guard, false)

	return deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Director: director,
		Vault:    vault,
		Guard:    guard,
	}), director
}

func decodeCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("expected a JSON body, got %q: %v", recorder.Body.String(), err)
	}

	code, _ := body["code"].(string)

	return code
}

// A deployment that belongs to one of the broker's service instances
// passes the ownership check, so every mutating route reaches the
// director.
func TestManagedDeploymentActionsReachTheDirector(t *testing.T) {
	t.Parallel()

	for _, action := range deploymentActions() {
		t.Run(action.name, func(t *testing.T) {
			t.Parallel()

			handler, director := ownershipHandler(managedVault())

			recorder := serveAction(t, handler, action)

			if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want the action to run: %s", recorder.Code, recorder.Body.String())
			}

			calls, _ := director.recorded()
			if len(calls) != 1 || calls[0] != action.call {
				t.Errorf("director calls = %v, want one %s", calls, action.call)
			}
		})
	}
}

// A deployment the index doesn't hold, such as the broker's own or one
// somebody else deployed to the same director, is refused with 403 and
// the code deployment_not_managed, and nothing reaches the director.
func TestUnmanagedDeploymentActionsAreRefused(t *testing.T) {
	t.Parallel()

	for _, action := range deploymentActions() {
		t.Run(action.name, func(t *testing.T) {
			t.Parallel()

			handler, director := ownershipHandler(managedVault())

			unmanaged := action
			unmanaged.path = strings.Replace(action.path, guardedDeployment, "cf", 1)

			recorder := serveAction(t, handler, unmanaged)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
			}

			if code := decodeCode(t, recorder); code != apierr.CodeDeploymentNotManaged {
				t.Errorf("code = %q, want %q", code, apierr.CodeDeploymentNotManaged)
			}

			if !strings.Contains(recorder.Body.String(), "cf") {
				t.Errorf("body = %s, want the message to name the deployment", recorder.Body.String())
			}

			calls, _ := director.recorded()
			if len(calls) != 0 {
				t.Errorf("director calls = %v, want none", calls)
			}
		})
	}
}

// An index that can't be read never allows the action. It answers 503
// with the code vault_unavailable, and so does a handler with no vault.
func TestDeploymentActionsAreRefusedWhenTheIndexIsUnreadable(t *testing.T) {
	t.Parallel()

	cases := map[string]*deployments.Handler{}

	unreadable := mutation.NewGuard(time.Now)
	cases["index read fails"] = deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Director: newRecordingDirector(unreadable, false),
		Vault:    &unreadableIndexVault{InstanceVault: *managedVault()},
		Guard:    unreadable,
	})

	guard := mutation.NewGuard(time.Now)
	cases["no vault"] = deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Director: newRecordingDirector(guard, false),
		Guard:    guard,
	})

	for name, handler := range cases {
		for _, action := range deploymentActions() {
			t.Run(name+" "+action.name, func(t *testing.T) {
				t.Parallel()

				recorder := serveAction(t, handler, action)

				if recorder.Code != http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
				}

				if code := decodeCode(t, recorder); code != apierr.CodeVaultUnavailable {
					t.Errorf("code = %q, want %q", code, apierr.CodeVaultUnavailable)
				}
			})
		}
	}
}

// unreadableIndexVault fails every read of the index, as a sealed Vault
// does.
type unreadableIndexVault struct {
	testutil.InstanceVault
}

func (v *unreadableIndexVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, errVaultDown
}

// A name that differs from a managed one in any way is another
// deployment. Encoded slashes, doubled slashes, dot segments, case, a
// trailing space, and a prefix of the managed name all fail to match, and
// none of them reaches the director.
func TestOddDeploymentNamesCannotSlipPastTheOwnershipCheck(t *testing.T) {
	t.Parallel()

	paths := map[string]string{
		"encoded slash":      "/b/deployments/" + guardedDeployment + "%2F..%2Fcf/restart",
		"encoded parent":     "/b/deployments/%2e%2e/cf/restart",
		"doubled slash":      "/b/deployments//cf/restart",
		"doubled after name": "/b/deployments/cf//restart",
		"dot segment":        "/b/deployments/./cf/restart",
		"upper case":         "/b/deployments/" + strings.ToUpper(guardedDeployment) + "/restart",
		"trailing space":     "/b/deployments/" + guardedDeployment + "%20/restart",
		"trailing newline":   "/b/deployments/" + guardedDeployment + "%0A/restart",
		"name prefix":        "/b/deployments/redis/restart",
		"name suffix":        "/b/deployments/x" + guardedDeployment + "/restart",
		"null byte":          "/b/deployments/" + guardedDeployment + "%00/restart",
	}

	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, director := ownershipHandler(managedVault())

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code == http.StatusOK || recorder.Code == http.StatusAccepted {
				t.Fatalf("status = %d, want a refusal: %s", recorder.Code, recorder.Body.String())
			}

			calls, _ := director.recorded()
			if len(calls) != 0 {
				t.Errorf("director calls = %v, want none", calls)
			}
		})
	}
}

// An encoded form of the managed name is the managed name, so it passes
// and the director sees the decoded name.
func TestEncodedManagedNameIsTheManagedName(t *testing.T) {
	t.Parallel()

	handler, director := ownershipHandler(managedVault())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/%72edis-7a1b/restart", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}

	calls, _ := director.recorded()
	if len(calls) != 1 || calls[0] != callRestart {
		t.Errorf("director calls = %v, want one RestartDeployment", calls)
	}
}

// ownEnvironmentConfig is a broker config whose environment is prod, so
// the broker's own deployment is prod-blacksmith.
type ownEnvironmentConfig struct{ writeTimeoutConfig }

func (ownEnvironmentConfig) GetEnvironment() string { return "prod" }

const ownDeployment = "prod-blacksmith"

func ownershipHandlerWithConfig(vault *testutil.InstanceVault) (*deployments.Handler, *recordingDirector) {
	guard := mutation.NewGuard(time.Now)
	director := newRecordingDirector(guard, false)

	return deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   ownEnvironmentConfig{},
		Director: director,
		Vault:    vault,
		Guard:    guard,
	}), director
}

// A manifest can hold credentials, so a read of a deployment the broker
// doesn't manage is refused as a change is, with 403 and the code
// deployment_not_managed.
func TestReadingAnUnmanagedDeploymentIsRefused(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/b/deployments/cf",
		"/b/deployments/cf/manifest",
		"/b/deployments/cf/errands",
		"/b/deployments/cf/vms",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			handler, _ := ownershipHandlerWithConfig(managedVault())

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
			}

			if code := decodeCode(t, recorder); code != apierr.CodeDeploymentNotManaged {
				t.Errorf("code = %q, want %q", code, apierr.CodeDeploymentNotManaged)
			}
		})
	}
}

// A read of a managed deployment runs.
func TestReadingAManagedDeploymentRuns(t *testing.T) {
	t.Parallel()

	handler, _ := ownershipHandlerWithConfig(managedVault())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+guardedDeployment+"/errands", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the read to run: %s", recorder.Code, recorder.Body.String())
	}
}

// The broker's own deployment is the environment name plus -blacksmith.
// It can be read, because the UI shows it, and it can't be changed,
// because it isn't a deployment of a service instance.
func TestBrokersOwnDeploymentIsReadableButNotChangeable(t *testing.T) {
	t.Parallel()

	handler, director := ownershipHandlerWithConfig(managedVault())

	read := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+ownDeployment+"/errands", nil)
	readRecorder := httptest.NewRecorder()

	handler.ServeHTTP(readRecorder, read)

	if readRecorder.Code == http.StatusForbidden || readRecorder.Code == http.StatusServiceUnavailable {
		t.Errorf("read status = %d, want the read to run: %s", readRecorder.Code, readRecorder.Body.String())
	}

	for _, action := range deploymentActions() {
		own := action
		own.path = strings.Replace(action.path, guardedDeployment, ownDeployment, 1)

		recorder := serveAction(t, handler, own)

		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s status = %d, want 403: %s", action.name, recorder.Code, recorder.Body.String())
		}
	}

	calls, _ := director.recorded()
	if len(calls) != 0 {
		t.Errorf("director calls = %v, want none", calls)
	}
}

// An index entry that records the broker's own deployment name does not
// make that deployment changeable. Every change is refused before the
// index is read, and the read still runs.
func TestIndexedOwnDeploymentIsStillNotChangeable(t *testing.T) {
	t.Parallel()

	for _, action := range deploymentActions() {
		t.Run(action.name, func(t *testing.T) {
			t.Parallel()

			handler, director := ownershipHandlerWithConfig(managingVault(ownDeployment))

			own := action
			own.path = strings.Replace(action.path, guardedDeployment, ownDeployment, 1)

			recorder := serveAction(t, handler, own)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
			}

			if code := decodeCode(t, recorder); code != apierr.CodeDeploymentNotManaged {
				t.Errorf("code = %q, want %q", code, apierr.CodeDeploymentNotManaged)
			}

			calls, _ := director.recorded()
			if len(calls) != 0 {
				t.Errorf("director calls = %v, want none", calls)
			}
		})
	}
}

// A change to the own deployment is refused even when the index can't be
// read, because the answer doesn't depend on the index.
func TestOwnDeploymentChangeIsRefusedWithoutReadingTheIndex(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)
	handler := deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   ownEnvironmentConfig{},
		Director: newRecordingDirector(guard, false),
		Vault:    &unreadableIndexVault{InstanceVault: *managingVault(ownDeployment)},
		Guard:    guard,
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/"+ownDeployment+"/restart", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}

// Only the exact own name is readable. Another environment's name, and a
// literal blacksmith deployment, are other deployments.
func TestOnlyTheBrokersOwnNameIsReadable(t *testing.T) {
	t.Parallel()

	for _, name := range []string{literalBlacksmith, "dev-blacksmith", ownDeployment + "-2", "x" + ownDeployment} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, _ := ownershipHandlerWithConfig(managedVault())

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+name, nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// With no environment configured the broker knows no own name, so no
// deployment is readable on that basis.
func TestNoOwnDeploymentWithoutAnEnvironment(t *testing.T) {
	t.Parallel()

	handler, _ := ownershipHandler(managedVault())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/-blacksmith", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
}

// A read answers 503 when the index can't be read, as an action does,
// except for the broker's own deployment, which needs no index.
func TestReadingIsRefusedWhenTheIndexIsUnreadable(t *testing.T) {
	t.Parallel()

	guard := mutation.NewGuard(time.Now)
	handler := deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.NewNoOpLogger(),
		Config:   ownEnvironmentConfig{},
		Director: newRecordingDirector(guard, false),
		Vault:    &unreadableIndexVault{InstanceVault: *managedVault()},
		Guard:    guard,
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/cf/manifest", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", recorder.Code, recorder.Body.String())
	}

	if code := decodeCode(t, recorder); code != apierr.CodeVaultUnavailable {
		t.Errorf("code = %q, want %q", code, apierr.CodeVaultUnavailable)
	}

	own := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+ownDeployment+"/errands", nil)
	ownRecorder := httptest.NewRecorder()

	handler.ServeHTTP(ownRecorder, own)

	if ownRecorder.Code == http.StatusForbidden || ownRecorder.Code == http.StatusServiceUnavailable {
		t.Errorf("own deployment status = %d, want the read to run", ownRecorder.Code)
	}
}

// A tombstone the reconciler left in the index owns nothing. Its
// deployment is refused for a read and for every change, as an
// unindexed name is.
func TestTombstonedEntriesManageNoDeployment(t *testing.T) {
	t.Parallel()

	marks := map[string]vaultPkg.Instance{
		"status":  {ID: ownedInstance, ServiceID: redisService, PlanID: redisService, DeploymentName: guardedDeployment, Status: vaultPkg.StatusDeleted},
		"deleted": {ID: ownedInstance, ServiceID: redisService, PlanID: redisService, DeploymentName: guardedDeployment, Deleted: true},
	}

	for mark, entry := range marks {
		t.Run(mark, func(t *testing.T) {
			t.Parallel()

			for _, action := range deploymentActions() {
				handler, director := ownershipHandler(&testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{ownedInstance: entry}})

				recorder := serveAction(t, handler, action)

				if recorder.Code != http.StatusForbidden {
					t.Errorf("%s status = %d, want 403: %s", action.name, recorder.Code, recorder.Body.String())
				}

				if calls, _ := director.recorded(); len(calls) != 0 {
					t.Errorf("%s director calls = %v, want none", action.name, calls)
				}
			}

			handler, _ := ownershipHandler(&testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{ownedInstance: entry}})

			read := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+guardedDeployment+"/manifest", nil)
			readRecorder := httptest.NewRecorder()

			handler.ServeHTTP(readRecorder, read)

			if readRecorder.Code != http.StatusForbidden {
				t.Errorf("read status = %d, want 403: %s", readRecorder.Code, readRecorder.Body.String())
			}
		})
	}
}

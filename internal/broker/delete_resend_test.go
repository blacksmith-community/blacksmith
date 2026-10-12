package broker_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

const (
	// resendTaskID is the task the test director names when it accepts
	// a delete.
	resendTaskID = 91

	// resendDirectorLogin is the user name and password the adapter
	// presents to the test director, which takes any login.
	resendDirectorLogin = "admin"
)

// resendDirector is a TLS test director, driven through the real director
// adapter, that shows a deployment until it says otherwise, answers each
// delete with the next answer it was given, and records what each delete
// carried.
type resendDirector struct {
	*boshtest.Director

	vault      *testutil.VaultDevServer
	instanceID string
	deployment string

	mu      sync.Mutex
	gone    bool
	answers []func(http.ResponseWriter, *http.Request)
	deletes []sentDelete
	checks  []int
}

// sentDelete is what the director saw of one delete: the context ID it
// carried, and the context ID the instance's task record held as it came.
type sentDelete struct {
	contextID string
	recorded  interface{}
}

func newResendDirector(t *testing.T, vault *testutil.VaultDevServer, answers ...func(http.ResponseWriter, *http.Request)) *resendDirector {
	t.Helper()

	director := &resendDirector{
		vault:      vault,
		instanceID: fmt.Sprintf("resend-%d", time.Now().UnixNano()),
		answers:    answers,
	}
	director.deployment = standalonePlanID + "-" + director.instanceID

	director.Director = boshtest.New(t, director.answer)

	return director
}

func (d *resendDirector) answer(writer http.ResponseWriter, request *http.Request) bool {
	deploymentPath := "/deployments/" + d.deployment

	switch {
	case request.Method == http.MethodGet && request.URL.Path == deploymentPath:
		d.mu.Lock()
		gone := d.gone
		d.mu.Unlock()

		if gone {
			boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, fmt.Sprintf("Deployment '%s' doesn't exist", d.deployment))

			return true
		}

		boshtest.WriteJSON(writer, http.StatusOK, map[string]interface{}{
			manifestNameKey: d.deployment, "manifest": "name: " + d.deployment + "\n",
			"releases": []string{}, "stemcells": []string{}, "teams": []string{},
		})

		return true
	case request.Method == http.MethodGet && request.URL.Path == "/tasks":
		d.mu.Lock()
		d.checks = append(d.checks, len(d.deletes))
		d.mu.Unlock()

		boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

		return true
	case request.Method == http.MethodGet && request.URL.Path == fmt.Sprintf("/tasks/%d", resendTaskID):
		boshtest.WriteJSON(writer, http.StatusOK, struct {
			ID          int    `json:"id"`
			State       string `json:"state"`
			Description string `json:"description"`
		}{ID: resendTaskID, State: outcomeTaskQueued, Description: "delete deployment " + d.deployment})

		return true
	case request.Method == http.MethodDelete && request.URL.Path == deploymentPath:
		record, _ := d.vault.ReadSecret(d.instanceID + "/task")

		d.mu.Lock()
		d.deletes = append(d.deletes, sentDelete{contextID: request.Header.Get("X-Bosh-Context-Id"), recorded: record["context_id"]})
		index := len(d.deletes) - 1
		d.mu.Unlock()

		if index >= len(d.answers) {
			boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the spec did not expect this delete")

			return true
		}

		d.answers[index](writer, request)

		return true
	default:
		return false
	}
}

// goneFromNowOn makes the director say the deployment does not exist.
func (d *resendDirector) goneFromNowOn() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.gone = true
}

func (d *resendDirector) sent() ([]sentDelete, []int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]sentDelete(nil), d.deletes...), append([]int(nil), d.checks...)
}

func (d *resendDirector) broker(t *testing.T) *broker.Broker {
	t.Helper()

	adapter, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  d.URL,
		Username: resendDirectorLogin,
		Password: resendDirectorLogin,
		CACert:   d.CACert,
	})
	if err != nil {
		t.Fatalf("failed to create the director adapter: %v", err)
	}

	return &broker.Broker{
		BOSH:          adapter,
		Vault:         internalVault.New(d.vault.Addr, d.vault.RootToken, true),
		Shield:        &shield.NoopClient{},
		Config:        &config.Config{},
		InstanceLocks: make(map[string]*sync.Mutex),
	}
}

func refuseDelete(writer http.ResponseWriter, _ *http.Request) {
	boshtest.WriteError(writer, http.StatusBadRequest, deleteRefusedCode, "director refused the delete")
}

func acceptDelete(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Location", fmt.Sprintf("/tasks/%d", resendTaskID))
	writer.WriteHeader(http.StatusFound)
}

// The broker resends a delete the director refused, under a context ID of
// its own that is on record before the resend goes out, and only after it
// has looked for a delete task already running on the deployment. A code
// 70000 on the last attempt is a deletion once the director's read of the
// deployment agrees, and a route 404 is never resent. Each case runs
// through the real director adapter against a TLS test director.
func TestRetryDeleteThroughTheRealAdapter(t *testing.T) {
	vault, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	defer vault.Close()

	t.Run("a refused delete is resent with a fresh context ID", func(t *testing.T) {
		resendRefusedDelete(t, vault)
	})

	t.Run("code 70000 on the last attempt is a deletion once the director confirms it", func(t *testing.T) {
		confirmLastNotFound(t, vault)
	})

	t.Run("a route 404 is never resent", func(t *testing.T) {
		neverResendRouteNotFound(t, vault)
	})
}

func resendRefusedDelete(t *testing.T, vault *testutil.VaultDevServer) {
	t.Helper()

	defer broker.SetDeleteRetryBackoff(time.Millisecond)()

	director := newResendDirector(t, vault, refuseDelete, acceptDelete)
	firstID := uuid.NewString()

	task, err := director.broker(t).RetryDeleteDeploymentTask(context.Background(), director.deployment, director.instanceID, firstID, 3)
	if err != nil {
		t.Fatalf("the delete failed: %v", err)
	}

	if task == nil || task.ID != resendTaskID {
		t.Fatalf("the delete settled on task %+v, want task %d", task, resendTaskID)
	}

	deletes, checks := director.sent()
	if len(deletes) != 2 {
		t.Fatalf("the director got %d deletes, want 2: %+v", len(deletes), deletes)
	}

	if deletes[0].contextID != firstID {
		t.Errorf("the first delete carried context ID %q, want %q", deletes[0].contextID, firstID)
	}

	if deletes[1].contextID == "" || deletes[1].contextID == firstID {
		t.Errorf("the resend carried context ID %q, which is not a fresh one", deletes[1].contextID)
	}

	if deletes[1].recorded != deletes[1].contextID {
		t.Errorf("the record held context ID %v while the resend carrying %q was sent", deletes[1].recorded, deletes[1].contextID)
	}

	if len(checks) != 1 || checks[0] != 1 {
		t.Errorf("the running tasks were listed after deletes %v, want once, between the two deletes", checks)
	}
}

func confirmLastNotFound(t *testing.T, vault *testutil.VaultDevServer) {
	t.Helper()

	var director *resendDirector

	director = newResendDirector(t, vault, func(writer http.ResponseWriter, _ *http.Request) {
		director.goneFromNowOn()
		boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, fmt.Sprintf("Deployment '%s' doesn't exist", director.deployment))
	})

	_, err := director.broker(t).RetryDeleteDeploymentTask(context.Background(), director.deployment, director.instanceID, uuid.NewString(), 1)
	if !errors.Is(err, bosh.ErrDeploymentNotFound) || errors.Is(err, broker.ErrFailedToDeleteDeployment) {
		t.Fatalf("the delete returned %v, want ErrDeploymentNotFound", err)
	}

	if deletes, _ := director.sent(); len(deletes) != 1 {
		t.Errorf("the director got %d deletes, want 1", len(deletes))
	}
}

func neverResendRouteNotFound(t *testing.T, vault *testutil.VaultDevServer) {
	t.Helper()

	defer broker.SetDeleteRetryBackoff(time.Minute)()

	director := newResendDirector(t, vault, func(writer http.ResponseWriter, request *http.Request) {
		boshtest.WriteRouteNotFound(writer, request)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := director.broker(t).RetryDeleteDeploymentTask(ctx, director.deployment, director.instanceID, uuid.NewString(), 3)
	if !boshapi.IsRouteNotFound(err) || errors.Is(err, bosh.ErrDeploymentNotFound) {
		t.Fatalf("the delete returned %v, want the route 404", err)
	}

	if strings.Contains(err.Error(), "context ended") {
		t.Errorf("the delete waited to resend after a route 404: %v", err)
	}

	if deletes, checks := director.sent(); len(deletes) != 1 || len(checks) != 0 {
		t.Errorf("the director got %d deletes and %d running-task lists, want 1 and 0", len(deletes), len(checks))
	}
}

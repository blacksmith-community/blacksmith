package broker_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// directorDetail stands for what a director's error can carry, such as
// its address and a release blob's path, which a tenant must never see.
const directorDetail = "https://10.0.0.6:25555/releases/blobs/9f1c"

// errDirectorDetail is a director error that carries directorDetail.
var errDirectorDetail = errors.New("the director answered 500 for " + directorDetail)

// requireNoDirectorDetail fails the spec unless the update settled as
// failed with a description that says what failed and carries nothing of
// the director's error.
func requireNoDirectorDetail(t *testing.T, answer osbapi.LastOperationResponse, says string) {
	t.Helper()

	if answer.State != osbapi.StateFailed || strings.Contains(answer.Description, directorDetail) || !strings.Contains(answer.Description, says) {
		t.Fatalf("the update settled as %+v, want failed, saying %q, and nothing of the director's error", answer, says)
	}
}

// A release upload, an update deploy, or a rollback deploy that the
// director refuses fails the update with a description of its own, and
// the director's error stays in the broker's log.
func TestUpdate_DirectorErrors_StayOutOfTheDescription(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("the release upload", func(t *testing.T) {
		t.Parallel()

		fixture := newUpdateFixture(t)
		script := &deployScript{}
		script.script(fixture, outcomeTaskDone)

		fixture.director.GetReleasesFn = func() ([]bosh.Release, error) { return nil, errDirectorDetail }

		instanceID := fixture.startFailingUpdate(ctx, t)
		requireNoDirectorDetail(t, fixture.settled(ctx, t, instanceID), "releases")
	})

	t.Run("the update's deploy", func(t *testing.T) {
		t.Parallel()

		fixture := newUpdateFixture(t)
		script := &deployScript{}
		script.script(fixture, outcomeTaskDone)

		fixture.director.UpdateDeploymentFn = func(string, string) (*bosh.Task, error) { return nil, errDirectorDetail }

		instanceID := fixture.startFailingUpdate(ctx, t)
		requireNoDirectorDetail(t, fixture.settled(ctx, t, instanceID), "did not take the deploy")
	})

	t.Run("the rollback's deploy", func(t *testing.T) {
		t.Parallel()

		fixture := newUpdateFixture(t)
		fixture.rollbackOn(ctx, t, true)

		script := &rollbackScript{}
		script.script(fixture, outcomeTaskDone)

		deploys := 0
		fixture.director.UpdateDeploymentFn = func(string, string) (*bosh.Task, error) {
			deploys++
			if deploys == 1 {
				return &bosh.Task{ID: updateTaskID, State: outcomeTaskProcessing}, nil
			}

			return nil, errDirectorDetail
		}

		instanceID := fixture.startFailingUpdate(ctx, t)
		requireNoDirectorDetail(t, fixture.settled(ctx, t, instanceID), "did not take its deploy")
	})
}

// numberedPlan reads its VM type from a size the parameters give as a
// number. A number stays plain where a string would become a literal, so
// a size the plan doesn't list fails the render with graft's text, which
// quotes the size.
const numberedPlan = "update-numbered"

// sizesKey and sizedVMType are where a sized plan lists its sizes and how
// it reads one. numberedSize is a size the numbered plan lists, and
// unlistedSize one it doesn't, whose digits stand for a parameter value no
// log may hold.
const (
	sizesKey     = "sizes"
	sizedVMType  = "(( grab meta.sizes[meta.params.size] ))"
	numberedSize = 1
	unlistedSize = 7041
	sizeParam    = "size"
)

// addNumberedPlan adds the numbered plan to the fixture's catalog.
func (f *updateFixture) addNumberedPlan() {
	open := f.broker.Plans[recordedNameService+"/"+openUpdatePlan]

	numbered := maps.Clone(open.Manifest)
	numbered["meta"] = map[string]interface{}{sizesKey: map[string]interface{}{strconv.Itoa(numberedSize): "vm-one"}}
	numbered["vm_type"] = sizedVMType

	open.ID, open.Name, open.Manifest = numberedPlan, numberedPlan, numbered
	f.broker.Plans[recordedNameService+"/"+numberedPlan] = open
}

// A render that fails names the parameter keys and the failure's class in
// the log, never graft's text, which can quote a parameter's value, and
// the platform gets a description of the broker's own.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestUpdate_RenderFailure_KeepsValuesOutOfLogAndAnswer(t *testing.T) {
	capture := newCaptureLogger()
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	fixture := newUpdateFixture(t)
	fixture.addNumberedPlan()

	ctx := context.Background()

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	// The stored size doesn't render, so the update is a change, and the
	// requested size does.
	instanceID := fixture.provisioned(ctx, t, numberedPlan, map[string]interface{}{sizeParam: unlistedSize})

	response, async, err := fixture.broker.Update(ctx, instanceID, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{sizeParam: numberedSize},
	}, true)
	requireAccepted(t, response, async, err)
	fixture.settled(ctx, t, instanceID)

	// The requested size doesn't render.
	other := fixture.provisioned(ctx, t, numberedPlan, map[string]interface{}{sizeParam: numberedSize})
	before := fixture.snapshot(ctx, t, other)

	_, _, err = fixture.broker.Update(ctx, other, osbapi.UpdateRequest{
		ServiceID: recordedNameService, Parameters: map[string]any{sizeParam: unlistedSize},
	}, true)
	if strings.Contains(fmt.Sprint(err), strconv.Itoa(unlistedSize)) || !strings.Contains(fmt.Sprint(err), "can't render") {
		t.Fatalf("the update whose render failed was answered %v, want the broker's own description and not the value", err)
	}

	fixture.requireRefused(ctx, t, other, before, err, isError(osbapi.ErrInvalidParameters))

	output := capture.output()
	if strings.Contains(output, strconv.Itoa(unlistedSize)) || strings.Count(output, "parameter keys size") != 2 {
		t.Fatalf("the log of the failed renders holds a parameter value or lacks a key name:\n%s", output)
	}
}

package broker_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

const (
	// hungRefreshBound is the credential refresh bound the spec sets. The
	// bound starts before the refresh reads the instance's index entry
	// from Vault, so it has to outlast that read even on a loaded host
	// running under coverage, or the refresh ends before it reaches the
	// hung director and the spec has nothing to see. A bound as short as
	// the follow specs' would fail that way.
	hungRefreshBound = time.Second

	// hungVMsLimit is how long a hung VM read waits when nothing ends it,
	// so a spec whose bound is missing fails rather than hangs. It stays
	// well beyond hungRefreshBound.
	hungVMsLimit = 10 * time.Second
)

// hungVMsDirector hangs every read of a deployment's VMs until the
// caller's context ends, and counts the reads that ended that way.
type hungVMsDirector struct {
	*testutil.ScriptedBOSHDirector

	bounded atomic.Int32
}

// GetDeploymentVMs hangs until ctx ends, or until hungVMsLimit passes.
func (d *hungVMsDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]bosh.VM, error) {
	select {
	case <-ctx.Done():
		d.bounded.Add(1)

		return nil, fmt.Errorf("reading the VMs of %s: %w", deployment, ctx.Err())
	case <-time.After(hungVMsLimit):
		return nil, fmt.Errorf("reading the VMs of %s: %w", deployment, errDirectorUnreachable)
	}
}

// A credential refresh that hangs after a successful deploy ends at its
// bound, so the update settles as succeeded with the stored credentials
// and releases its claim and the settle lock.
func TestUpdate_CredentialRefreshHangs_SettlesAtItsBound(t *testing.T) {
	t.Parallel()

	fixture := newUpdateFixture(t)
	ctx := context.Background()

	script := &deployScript{}
	script.script(fixture, outcomeTaskDone)

	hung := &hungVMsDirector{ScriptedBOSHDirector: fixture.director}
	fixture.broker.BOSH = hung
	fixture.broker.SetUpdateCredentialsBound(hungRefreshBound)

	instanceID := fixture.startFailingUpdate(ctx, t)

	if answer := fixture.settled(ctx, t, instanceID); answer.State != osbapi.StateSucceeded {
		t.Fatalf("the update settled as %+v, want succeeded", answer)
	}

	if hung.bounded.Load() == 0 {
		t.Fatal("the hung credential read ran until the spec's own limit, want it ended at its bound")
	}

	fixture.requireReleased(t, instanceID)
}

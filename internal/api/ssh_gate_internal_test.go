package api

import (
	"context"
	"errors"
	"testing"

	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade"
)

var (
	errInnerRan  = errors.New("inner runner ran")
	errNoSession = errors.New("no session")
)

// sshSwitch is a configuration that answers only the SSH switch. Any other
// call panics on the nil embedded interface, which the gate must not make.
type sshSwitch struct {
	interfaces.Config

	enabled bool
}

func (s sshSwitch) IsSSHEnabled() bool { return s.enabled }

// A runner for a task that was created before ssh.enabled was turned off
// opens no session, and the wrapped runner is never reached.
func TestGateRunnerRefusesWhenSSHIsOff(t *testing.T) {
	t.Parallel()

	calls := 0

	inner := func(context.Context, string, string, []int) (upgrade.PatchSession, error) {
		calls++

		return nil, errNoSession
	}

	session, err := gateRunner(sshSwitch{enabled: false}, inner)(context.Background(), "dep", "group", []int{0})
	if !errors.Is(err, errSSHDisabled) {
		t.Errorf("error = %v, want errSSHDisabled", err)
	}

	if session != nil {
		t.Error("a session was returned with SSH off")
	}

	if calls != 0 {
		t.Errorf("the wrapped runner ran %d times with SSH off", calls)
	}
}

// With ssh.enabled true the gate hands the call straight to the runner.
func TestGateRunnerPassesThroughWhenSSHIsOn(t *testing.T) {
	t.Parallel()

	want := errInnerRan

	inner := func(context.Context, string, string, []int) (upgrade.PatchSession, error) {
		return nil, want
	}

	_, err := gateRunner(sshSwitch{enabled: true}, inner)(context.Background(), "dep", "group", nil)
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want the wrapped runner's error", err)
	}
}

package testutil

import (
	"os/exec"
	"syscall"
)

// Test-only exports for the subprocess helpers, so the external test
// package can exercise them without widening the public API.

// PickFreePort exposes pickFreePort.
func PickFreePort(next func() (int, error)) (int, error) { return pickFreePort(next) }

// DevServerEnv exposes devServerEnv.
func DevServerEnv(environ []string, home string) []string { return devServerEnv(environ, home) }

// FindVaultBinary exposes findVaultBinary.
func FindVaultBinary() (string, error) { return findVaultBinary() }

// Pid returns the subprocess id of the dev server.
func (v *VaultDevServer) Pid() int {
	if v.cmd == nil || v.cmd.Process == nil {
		return 0
	}

	return v.cmd.Process.Pid
}

// NewFakeGroupServer starts a shell script as a dev-server stand-in, using the
// same process-group setup and reaping as the real helper, without readiness.
func NewFakeGroupServer(script string) (*VaultDevServer, error) {
	out := &lockedBuffer{}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	srv := &VaultDevServer{cmd: cmd, done: make(chan struct{}), out: out}

	go func() {
		_ = cmd.Wait()

		close(srv.done)
	}()

	return srv, nil
}

// Exited reports whether the leader process has been reaped.
func (v *VaultDevServer) Exited() bool { return v.exited() }

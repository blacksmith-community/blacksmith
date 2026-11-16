package main

import (
	"testing"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/services/rabbitmq"
	internalVault "blacksmith/internal/vault"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// With ssh.enabled false the broker builds nothing that opens an SSH
// session, so a route that is added later without a gate still has no
// service to reach. The services that never touch SSH are still built.
func TestSSHServicesAreNotBuiltWhenSSHIsOff(t *testing.T) {
	t.Parallel()

	off := false
	cfg := &config.Config{SSH: config.SSHConfig{Enabled: &off}}
	vault := internalVault.New("https://127.0.0.1:1", "", true)

	// A director is present, so only the switch keeps the SSH service from
	// being built.
	sshService, rabbitSSH, metadata, executor, audit, pluginsMetadata, pluginsExecutor, pluginsAudit, webSocket :=
		initializeServices(cfg, &broker.Broker{BOSH: testutil.NewMockBOSHDirector()}, vault, &loggerPkg.NoOpLogger{})

	if sshService != nil || rabbitSSH != nil || executor != nil || pluginsExecutor != nil || webSocket != nil {
		t.Errorf("SSH services were built with SSH off: ssh=%v rabbitmq-ssh=%v executor=%v plugins-executor=%v websocket=%v",
			sshService != nil, rabbitSSH != nil, executor != nil, pluginsExecutor != nil, webSocket != nil)
	}

	if metadata == nil || audit == nil || pluginsMetadata == nil || pluginsAudit == nil {
		t.Error("a RabbitMQ service that does not use SSH was not built")
	}
}

// The same broker with ssh.enabled true does build the SSH service, so the
// test above proves the switch and not a missing director.
func TestSSHServicesAreBuiltWhenSSHIsOn(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	vault := internalVault.New("https://127.0.0.1:1", "", true)

	built := func() bool {
		sshService, _, _, _, _, _, _, _, _ := initializeServices(cfg, &broker.Broker{BOSH: testutil.NewMockBOSHDirector()}, vault, &loggerPkg.NoOpLogger{})

		return sshService != nil
	}

	if !built() {
		t.Error("the SSH service was not built with SSH on and a director present")
	}
}

// A service pointer that was never built reaches the API as a true nil, so
// the handlers' nil checks answer 503 instead of calling through it.
func TestUnbuiltSSHServicesReachTheAPIAsNil(t *testing.T) {
	t.Parallel()

	sshAPI := orNil[interfaces.SSHService, ssh.ServiceImpl](nil)
	rabbitAPI := orNil[interfaces.RabbitMQSSHService, rabbitmq.SSHService](nil)

	if sshAPI != nil || rabbitAPI != nil {
		t.Errorf("a nil pointer became a non-nil interface: ssh=%v rabbitmq-ssh=%v", sshAPI != nil, rabbitAPI != nil)
	}
}

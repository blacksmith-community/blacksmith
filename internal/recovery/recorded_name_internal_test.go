package recovery

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/broker"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// serviceKey is the index entry field that records an instance's service.
const serviceKey = "service_id"

// lineLogger keeps every line logged at info level and above.
type lineLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (l *lineLogger) Info(msg string, args ...interface{})    { l.record(msg, args) }
func (l *lineLogger) Warning(msg string, args ...interface{}) { l.record(msg, args) }
func (l *lineLogger) Error(msg string, args ...interface{})   { l.record(msg, args) }

func (l *lineLogger) record(msg string, args []interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(msg, args...))
}

func (l *lineLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// After a plan change, the deployment keeps the name it was created under,
// so credential recovery reads the manifest of the deployment the index
// entry records, never one named from the entry's current plan.
func TestDeploymentName_ReadsRecordedName(t *testing.T) {
	t.Parallel()

	const (
		instanceID  = "recovery-recorded-name"
		serviceID   = "redis"
		currentPlan = "redis-large"
		recorded    = "redis-small-" + instanceID
		minted      = currentPlan + "-" + instanceID
	)

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	err = vaultClient.Index(t.Context(), instanceID, map[string]interface{}{
		serviceKey:      serviceID,
		planKey:         currentPlan,
		recordedNameKey: recorded,
	})
	if err != nil {
		t.Fatalf("failed to seed the index: %v", err)
	}

	brokerInstance := &broker.Broker{
		BOSH:  testutil.NewScriptedBOSHDirector(),
		Plans: map[string]services.Plan{serviceID + "/" + currentPlan: {ID: currentPlan, Name: "large"}},
	}

	recovery := NewCredentialRecovery(brokerInstance, vaultClient)
	lines := &lineLogger{NoOpLogger: &logger.NoOpLogger{}}
	recovery.logger = lines

	_, _ = recovery.recoverFromBOSH(t.Context(), instanceID)

	logged := lines.joined()
	if !strings.Contains(logged, recorded) {
		t.Errorf("recovery never named the recorded deployment %s; it logged:\n%s", recorded, logged)
	}

	if strings.Contains(logged, minted) {
		t.Errorf("recovery named %s, a deployment built from the current plan; it logged:\n%s", minted, logged)
	}
}

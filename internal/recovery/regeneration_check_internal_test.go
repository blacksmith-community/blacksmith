package recovery

import (
	"errors"
	"fmt"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// errDirectorUnreachable is a director that cannot be reached.
var errDirectorUnreachable = errors.New("dial tcp 127.0.0.1:25555: connect: connection refused")

// Only the director's deployment-not-found answer means the deployment is
// gone. The regeneration check reports any other failed lookup as the
// director's error, so a director that cannot be reached is not reported as
// a missing deployment.
func TestRegenerationCheckReportsOnlyNotFoundAsAMissingDeployment(t *testing.T) {
	t.Parallel()

	const (
		instanceID = "regen"
		planID     = "redis-standalone"
		deployment = planID + "-" + instanceID
	)

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	err = vaultClient.Index(t.Context(), instanceID, map[string]interface{}{planKey: planID})
	if err != nil {
		t.Fatalf("failed to seed %s: %v", instanceID, err)
	}

	answers := []struct {
		name     string
		answer   error
		notFound bool
	}{
		{"the director's deployment-not-found answer", fmt.Errorf("deployment %s: %w", deployment, bosh.ErrDeploymentNotFound), true},
		{"a director that cannot be reached", errDirectorUnreachable, false},
	}

	for _, directorAnswer := range answers {
		t.Run(directorAnswer.name, func(t *testing.T) {
			t.Parallel()

			director := testutil.NewScriptedBOSHDirector()
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) {
				return nil, directorAnswer.answer
			}

			recovery := NewCredentialRecovery(&broker.Broker{BOSH: director}, vaultClient)

			err := recovery.checkRegenerationPossible(t.Context(), instanceID)
			if err == nil {
				t.Fatalf("the check passed although the director answered %v", directorAnswer.answer)
			}

			if errors.Is(err, ErrDeploymentNotFound) != directorAnswer.notFound {
				t.Errorf("the check answered %v; reported as a missing deployment: %t, want %t",
					err, errors.Is(err, ErrDeploymentNotFound), directorAnswer.notFound)
			}

			if !directorAnswer.notFound && !errors.Is(err, errDirectorUnreachable) {
				t.Errorf("the check answered %v, which does not carry the director's error", err)
			}
		})
	}
}

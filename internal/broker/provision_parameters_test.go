package broker_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// schemaPlanID is a plan whose schema declares the one parameter it reads.
const schemaPlanID = "schema-plan"

// withParameterSchema gives each plan a schema that accepts only
// maxmemory-policy, as one of two strings.
func withParameterSchema(plans map[string]services.Plan) map[string]services.Plan {
	for key, plan := range plans {
		plan.Schemas = &services.Schemas{Parameters: map[string]interface{}{
			"$schema": "http://json-schema.org/draft-04/schema#",
			"type":    "object",
			"properties": map[string]interface{}{
				"maxmemory-policy": map[string]interface{}{"type": "string", "enum": []interface{}{"noeviction", "allkeys-lru"}},
			},
		}}
		plans[key] = plan
	}

	return plans
}

// A provision whose parameters the plan's schema refuses gets a 400
// before anything is written or deployed.
func TestProvision_InvalidParameters_400(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	t.Cleanup(server.Close)

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, bosh.ErrDeploymentNotFound }
	brokerInstance := &broker.Broker{BOSH: director, Vault: vaultClient, Plans: withParameterSchema(deployablePlans(t, schemaPlanID))}
	ctx := context.Background()

	for name, params := range map[string]map[string]any{
		"undeclared key": {"maxmemory": "1gb"},
		"refused value":  {"maxmemory-policy": "volatile-ttl"},
	} {
		instanceID := "invalid-params-" + strings.ReplaceAll(name, " ", "-")

		_, _, err = brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: recordedNameService, PlanID: schemaPlanID, Parameters: params,
		}, true)
		if !errors.Is(err, osbapi.ErrInvalidParameters) {
			t.Fatalf("%s: the provision was answered %v, want ErrInvalidParameters", name, err)
		}

		var stored map[string]interface{}

		exists, getErr := vaultClient.Get(ctx, instanceID, &stored)
		if getErr != nil || exists {
			t.Fatalf("%s: the refused provision wrote its instance record (exists %v, err %v)", name, exists, getErr)
		}

		_, indexed, findErr := vaultClient.FindInstance(ctx, instanceID)
		if findErr != nil || indexed {
			t.Fatalf("%s: the refused provision indexed the instance (indexed %v, err %v)", name, indexed, findErr)
		}
	}

	if calls := director.Calls("CreateDeployment"); len(calls) != 0 {
		t.Fatalf("a refused provision sent %d deploys", len(calls))
	}
}

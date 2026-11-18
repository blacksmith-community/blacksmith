package bosh_test

import (
	"testing"
	"time"

	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/pkg/logger"
)

// A plan whose parameter schema the catalog left out says why in its
// /b/status summary, which the Attention required box reads, and a plan
// whose schema is published says nothing.
func TestStatusNamesAPlanWhoseSchemaWasRefused(t *testing.T) {
	t.Parallel()

	const reason = "the parameter schema refers to a schema outside itself"

	for _, test := range []struct {
		name    string
		refused string
	}{
		{"a refused schema", reason},
		{"a published schema", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := boshhandler.NewHandler(boshhandler.Dependencies{
				Logger:                    &logger.NoOpLogger{},
				Config:                    statusConfig{},
				Director:                  &blockingDirector{},
				Broker:                    planBroker{refused: test.refused},
				DeploymentReleasesTimeout: time.Hour,
				Vault:                     &statusVault{},
			})

			got, named := status(t, handler).Plans[redisPlan]["schema_refused"]
			if test.refused == "" && named {
				t.Fatalf("a published schema's plan names schema_refused %v", got)
			}

			if test.refused != "" && got != test.refused {
				t.Fatalf("the plan's schema_refused is %v, want %q", got, test.refused)
			}
		})
	}
}

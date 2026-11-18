package bosh_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

// attentionBroker is a broker that reports owed cleanups it refused.
type attentionBroker struct {
	planBroker

	entries []interfaces.CleanupAttention
}

func (b attentionBroker) CleanupAttention() []interfaces.CleanupAttention { return b.entries }

// A failed provision whose owed cleanup the broker refused is named in
// /b/status, which the Attention required box reads, and a broker with
// none, or one that cannot report them, adds nothing.
func TestStatusNamesTheOwedCleanupsTheBrokerRefused(t *testing.T) {
	t.Parallel()

	refused := interfaces.CleanupAttention{
		InstanceID:     "instance-1",
		DeploymentName: "standalone-another-instance",
		Reason:         "Deployment standalone-another-instance was not named for this instance, so the broker does not delete it",
		Since:          time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
	}

	for _, test := range []struct {
		name   string
		broker interfaces.Broker
		want   []interfaces.CleanupAttention
	}{
		{"a refused cleanup", attentionBroker{entries: []interfaces.CleanupAttention{refused}}, []interfaces.CleanupAttention{refused}},
		{"no refused cleanup", attentionBroker{}, nil},
		{"a broker that reports none", planBroker{}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			handler := boshhandler.NewHandler(boshhandler.Dependencies{
				Logger:                    &logger.NoOpLogger{},
				Config:                    statusConfig{},
				Director:                  &blockingDirector{},
				Broker:                    test.broker,
				Vault:                     &statusVault{},
				DeploymentReleasesTimeout: time.Hour,
			})

			recorder := getWithin(t, waitLimit, handler.GetStatus, "/b/status")
			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", recorder.Code)
			}

			var answer struct {
				CleanupAttention []interfaces.CleanupAttention `json:"cleanup_attention"`
			}

			err := json.Unmarshal(recorder.Body.Bytes(), &answer)
			if err != nil {
				t.Fatalf("decoding the status: %v", err)
			}

			if len(answer.CleanupAttention) != len(test.want) {
				t.Fatalf("cleanup_attention is %+v, want %+v", answer.CleanupAttention, test.want)
			}

			for index, entry := range test.want {
				if !answer.CleanupAttention[index].Since.Equal(entry.Since) || answer.CleanupAttention[index].InstanceID != entry.InstanceID ||
					answer.CleanupAttention[index].DeploymentName != entry.DeploymentName || answer.CleanupAttention[index].Reason != entry.Reason {
					t.Fatalf("cleanup_attention[%d] is %+v, want %+v", index, answer.CleanupAttention[index], entry)
				}
			}
		})
	}
}

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

// updateAttentionBroker is a broker that reports updates it couldn't
// carry through.
type updateAttentionBroker struct {
	planBroker

	entries []interfaces.UpdateAttention
}

func (b updateAttentionBroker) UpdateAttention() []interfaces.UpdateAttention { return b.entries }

// An update the broker couldn't carry through is named in /b/status,
// which the Attention required box reads, and a broker with none, or one
// that cannot report them, adds nothing.
func TestStatusNamesTheUpdatesTheBrokerCouldNotCarryThrough(t *testing.T) {
	t.Parallel()

	stuck := interfaces.UpdateAttention{
		InstanceID:     "instance-1",
		DeploymentName: "standalone-instance-1",
		Reason:         "Plan standalone is no longer in the catalog, so the update settled as failed, and every update of the instance is refused until an operator restores the plan",
		Since:          time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
	}

	for _, test := range []struct {
		name   string
		broker interfaces.Broker
		want   []interfaces.UpdateAttention
	}{
		{"an update it couldn't carry through", updateAttentionBroker{entries: []interfaces.UpdateAttention{stuck}}, []interfaces.UpdateAttention{stuck}},
		{"no such update", updateAttentionBroker{}, nil},
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
				UpdateAttention []interfaces.UpdateAttention `json:"update_attention"`
			}

			err := json.Unmarshal(recorder.Body.Bytes(), &answer)
			if err != nil {
				t.Fatalf("decoding the status: %v", err)
			}

			if len(answer.UpdateAttention) != len(test.want) {
				t.Fatalf("update_attention is %+v, want %+v", answer.UpdateAttention, test.want)
			}

			for index, entry := range test.want {
				got := answer.UpdateAttention[index]
				if !got.Since.Equal(entry.Since) || got.InstanceID != entry.InstanceID || got.DeploymentName != entry.DeploymentName || got.Reason != entry.Reason {
					t.Fatalf("update_attention[%d] is %+v, want %+v", index, got, entry)
				}
			}
		})
	}
}

package deployments_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// instancesDirector lists the instances it holds for any deployment.
type instancesDirector struct {
	*testutil.MockBOSHDirector

	instances []bosh.Instance
}

func (d *instancesDirector) GetInstances(context.Context, string) ([]bosh.Instance, error) {
	return d.instances, nil
}

// An instance the director lists without an index, which the listing gives
// index "-1", is named group/<uuid>, because group/-1 names no instance.
// An instance with an index keeps group/index. Every instance also carries
// its UUID as id.
func TestInstancesHandlerNamesAnIndexlessInstanceByID(t *testing.T) {
	t.Parallel()

	director := &instancesDirector{
		MockBOSHDirector: testutil.NewMockBOSHDirector(),
		instances: []bosh.Instance{
			{ID: "uuid-0", Group: "node", Index: "0", State: "started"},
			{ID: "uuid-1", Group: "node", Index: "-1", State: "started"},
		},
	}
	handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger(), Director: director})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/valkey-1/instances", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Instances []struct {
			Instance string `json:"instance"`
			ID       string `json:"id"`
		} `json:"instances"`
	}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decoding the answer %q: %v", recorder.Body.String(), err)
	}

	want := []struct{ instance, id string }{{"node/0", "uuid-0"}, {"node/uuid-1", "uuid-1"}}
	if len(body.Instances) != len(want) {
		t.Fatalf("expected %d instances, got %+v", len(want), body.Instances)
	}

	for index, instance := range body.Instances {
		if instance.Instance != want[index].instance || instance.ID != want[index].id {
			t.Errorf("instance %d is %q with id %q, want %q with id %q",
				index, instance.Instance, instance.ID, want[index].instance, want[index].id)
		}
	}
}

package bosh_test

import (
	"encoding/json"
	"net/http"
	"testing"

	boshhandler "blacksmith/internal/handlers/bosh"
	"blacksmith/pkg/logger"
)

// statusBody requests /b/status from a handler built with the build values
// and decodes the whole answer.
func statusBody(t *testing.T, version, commit string) map[string]interface{} {
	t.Helper()

	handler := boshhandler.NewHandler(boshhandler.Dependencies{
		Logger:  &logger.NoOpLogger{},
		Config:  statusConfig{},
		Version: version,
		Commit:  commit,
	})

	recorder := getWithin(t, waitLimit, handler.GetStatus, "/b/status")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decoding the status: %v", err)
	}

	return body
}

func TestStatusReportsTheBuildVersionAndCommit(t *testing.T) {
	t.Parallel()

	body := statusBody(t, "1.8.0", "abc1234")

	if body["version"] != "1.8.0" {
		t.Fatalf("expected version 1.8.0, got %v", body["version"])
	}

	if body["commit"] != "abc1234" {
		t.Fatalf("expected commit abc1234, got %v", body["commit"])
	}
}

func TestStatusReportsTheDevelopmentVersionWhenNoneWasInjected(t *testing.T) {
	t.Parallel()

	body := statusBody(t, "", "")

	if body["version"] != "(development version)" {
		t.Fatalf("expected the development version, got %v", body["version"])
	}

	if body["commit"] != "unknown" {
		t.Fatalf("expected commit unknown, got %v", body["commit"])
	}
}

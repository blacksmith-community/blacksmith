package response_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/pkg/http/response"
)

func TestWriteErrorCodeEscapesQuotes(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	response.WriteErrorCode(rec, http.StatusConflict, "instance_busy", `held by "upgrade"`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}

	var body map[string]interface{}

	err := json.Unmarshal(rec.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	if body["success"] != false || body["code"] != "instance_busy" || body["error"] != `held by "upgrade"` {
		t.Fatalf("unexpected body: %v", body)
	}
}

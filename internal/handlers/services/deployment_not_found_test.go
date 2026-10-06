package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

var errDirectorAnswered500 = errors.New("director responded with non-successful status code '500' response 'lookup of task 404 failed'")

// deploymentLookup is one of the two handler steps that ask the director for
// a deployment and answer for a deleted one when it is gone.
type deploymentLookup struct {
	name  string
	fetch func(h *Handler, recorder *httptest.ResponseRecorder, deploymentName string) (*bosh.DeploymentDetail, error)
}

func deploymentLookups() []deploymentLookup {
	return []deploymentLookup{
		{
			name: "manifest",
			fetch: func(h *Handler, recorder *httptest.ResponseRecorder, deploymentName string) (*bosh.DeploymentDetail, error) {
				return h.fetchDeploymentManifest(context.Background(), recorder, logger.Get().Named("test"), deploymentName)
			},
		},
		{
			name: "details",
			fetch: func(h *Handler, recorder *httptest.ResponseRecorder, deploymentName string) (*bosh.DeploymentDetail, error) {
				return h.fetchDeploymentForDetails(context.Background(), recorder, logger.Get().Named("test"), deploymentName, "instance-1")
			},
		},
	}
}

func handlerWithDeploymentError(deploymentErr error) *Handler {
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, deploymentErr }

	return NewHandler(Dependencies{Logger: logger.Get().Named("test"), Director: director})
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("response body is not JSON: %v: %q", err, recorder.Body.String())
	}

	return body
}

// An error whose text happens to contain "404", from an answer that was not a
// 404, is a failed lookup and not a deleted deployment.
func TestDeploymentLookupKeepsA404InErrorTextAsAnError(t *testing.T) {
	t.Parallel()

	for _, lookup := range deploymentLookups() {
		t.Run(lookup.name, func(t *testing.T) {
			t.Parallel()

			deploymentErr := fmt.Errorf("failed to get deployment redis-404: %w", errDirectorAnswered500)
			recorder := httptest.NewRecorder()

			_, err := lookup.fetch(handlerWithDeploymentError(deploymentErr), recorder, "redis-404")
			if err == nil {
				t.Fatal("the lookup returned no error")
			}

			if !errors.Is(err, errDirectorAnswered500) {
				t.Fatalf("the lookup error does not wrap the director's error: %v", err)
			}

			body := decodeBody(t, recorder)
			if _, deleted := body["deleted"]; deleted {
				t.Fatalf("a 500 answer was reported as a deleted deployment: %v", body)
			}

			if body["error"] != deploymentErr.Error() {
				t.Fatalf("the response error is %v, want %q", body["error"], deploymentErr.Error())
			}
		})
	}
}

// The director's not-found sentinel still reads as a deleted deployment.
func TestDeploymentLookupReportsAMissingDeploymentAsDeleted(t *testing.T) {
	t.Parallel()

	for _, lookup := range deploymentLookups() {
		t.Run(lookup.name, func(t *testing.T) {
			t.Parallel()

			deploymentErr := fmt.Errorf("%w: redis-1: director responded with non-successful status code '404'", bosh.ErrDeploymentNotFound)
			recorder := httptest.NewRecorder()

			_, err := lookup.fetch(handlerWithDeploymentError(deploymentErr), recorder, "redis-1")
			if !errors.Is(err, bosh.ErrDeploymentNotFound) {
				t.Fatalf("the lookup error is %v, want one wrapping ErrDeploymentNotFound", err)
			}

			body := decodeBody(t, recorder)
			if body["deleted"] != true {
				t.Fatalf("a missing deployment was not reported as deleted: %v", body)
			}
		})
	}
}

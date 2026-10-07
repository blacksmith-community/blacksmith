package configuration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

var errDirectorUnavailable = errors.New("the director is unavailable")

// diffDirector is a director whose config diff answers with a fixed error.
type diffDirector struct {
	*testutil.IntegrationMockBOSH

	diffErr error
}

func (d *diffDirector) ComputeConfigDiff(context.Context, string, string) (*bosh.ConfigDiff, error) {
	return nil, d.diffErr
}

func diffStatus(t *testing.T, diffErr error) int {
	t.Helper()

	handler := NewHandler(Dependencies{
		Logger:   logger.Get().Named("test"),
		Director: &diffDirector{IntegrationMockBOSH: testutil.NewIntegrationMockBOSH(), diffErr: diffErr},
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/b/configs/cloud/default/diff?from=1&to=99", nil)

	handler.ServeHTTP(recorder, request)

	return recorder.Code
}

// A diff against a config ID the director does not hold answers 404, the way
// the config detail endpoint does, and a director failure still answers 500.
func TestConfigDiffAnswersNotFoundForAMissingConfigID(t *testing.T) {
	t.Parallel()

	missing := fmt.Errorf("%w: the director answered 404", bosh.ErrConfigNotFound)
	if got := diffStatus(t, missing); got != http.StatusNotFound {
		t.Errorf("expected 404 for a missing config ID, got %d", got)
	}

	if got := diffStatus(t, errDirectorUnavailable); got != http.StatusInternalServerError {
		t.Errorf("expected 500 for a director failure, got %d", got)
	}
}

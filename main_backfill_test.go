package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// errDirectorListFailed is the failure of a director's deployment list.
var errDirectorListFailed = errors.New("director unavailable")

// The deployment-name backfill at boot never stops the broker. A director
// that can't list its deployments leaves a warning, and the next boot runs
// the backfill again. With no director, the backfill is skipped with a
// warning as well.
func TestBackfillDeploymentNamesNeverStopsBoot(t *testing.T) {
	t.Parallel()

	t.Run("a director that can't list its deployments", func(t *testing.T) {
		t.Parallel()

		director := testutil.NewScriptedBOSHDirector()
		director.GetDeploymentsFn = func() ([]bosh.Deployment, error) {
			return nil, errDirectorListFailed
		}

		pool := bosh.NewPooledDirector(director, 1, time.Second, loggerPkg.CreateSilentLogger())
		logger := newRecordingLogger()

		backfillDeploymentNames(context.Background(), &broker.Broker{BOSH: pool}, pool, logger)

		warnings := strings.Join(logger.lines("warn"), "\n")
		if !strings.Contains(warnings, "director unavailable") {
			t.Errorf("expected a warning that names the director's failure, got %q", warnings)
		}
	})

	t.Run("no director", func(t *testing.T) {
		t.Parallel()

		logger := newRecordingLogger()

		backfillDeploymentNames(context.Background(), &broker.Broker{}, nil, logger)

		if warnings := logger.lines("warn"); len(warnings) != 1 {
			t.Errorf("expected one warning, got %q", warnings)
		}
	})
}

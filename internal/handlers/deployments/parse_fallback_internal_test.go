package deployments

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/pkg/logger"
)

// warnRecorder keeps the warnings it is given. Named hands back the same
// recorder, so a handler's child logger records here too.
type warnRecorder struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	warns []string
}

func (r *warnRecorder) Named(string) logger.Logger { return r }

func (r *warnRecorder) Warn(msg string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.warns = append(r.warns, fmt.Sprintf(msg, args...))
}

// TestReadManifestLogsAParseFailure checks that a manifest that does not
// parse still answers with its text, and that the fallback leaves a warning
// that names the deployment and the error but holds none of the manifest.
func TestReadManifestLogsAParseFailure(t *testing.T) {
	t.Parallel()

	const secret = "s3cret-value"

	recorder := &warnRecorder{NoOpLogger: &logger.NoOpLogger{}}
	handler := NewHandler(Dependencies{Logger: recorder, Director: &manifestDirector{manifest: "name: [" + secret + "\n"}})

	got, err := handler.readManifest(context.Background(), "parity")
	if err != nil {
		t.Fatal(err)
	}

	if parsed, ok := got["parsed"].(map[string]interface{}); !ok || len(parsed) != 0 {
		t.Errorf("parsed = %v, want an empty map", got["parsed"])
	}

	if len(recorder.warns) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(recorder.warns), recorder.warns)
	}

	if !strings.Contains(recorder.warns[0], "parity") || strings.Contains(recorder.warns[0], secret) {
		t.Errorf("the warning should name the deployment and omit the manifest: %q", recorder.warns[0])
	}
}

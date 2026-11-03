package blacksmith_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
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

// TestFormatManifestResponseLogsAParseFailure checks that a manifest that
// does not parse still answers with its text, and that the fallback leaves a
// warning that names the error but holds none of the manifest.
func TestFormatManifestResponseLogsAParseFailure(t *testing.T) {
	t.Parallel()

	recorder := &warnRecorder{NoOpLogger: &logger.NoOpLogger{}}
	handler := handlerpkg.NewHandler(handlerpkg.Dependencies{Logger: recorder, Config: createTestConfig()})

	const secret = "s3cret-value"

	got := handler.FormatManifestResponse("name: [" + secret + "\n")

	if parsed, ok := got["parsed"].(map[string]interface{}); !ok || len(parsed) != 0 {
		t.Errorf("parsed = %v, want an empty map", got["parsed"])
	}

	if len(recorder.warns) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(recorder.warns), recorder.warns)
	}

	if strings.Contains(recorder.warns[0], secret) {
		t.Errorf("the warning holds manifest text: %q", recorder.warns[0])
	}
}

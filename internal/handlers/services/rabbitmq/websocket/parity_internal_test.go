package websocket

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// manifestVault answers every read with one manifest.
type manifestVault struct {
	testutil.InstanceVault

	manifest string
}

func (v *manifestVault) Get(_ context.Context, _ string, out interface{}) (bool, error) {
	body, err := json.Marshal(map[string]string{"manifest": v.manifest})
	if err != nil {
		return false, err //nolint:wrapcheck // a test double
	}

	return true, json.Unmarshal(body, out) //nolint:wrapcheck // a test double
}

// TestInstanceGroupFromManifestParity records the instance group name the
// handler finds in each manifest.
func TestInstanceGroupFromManifestParity(t *testing.T) {
	t.Parallel()

	log := logger.Get().Named("test")

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		handler := NewHandler(Dependencies{Logger: log, Vault: &manifestVault{manifest: doc.Body}})
		recorded.WriteString(parity.Section(doc.Name, handler.extractInstanceGroupFromManifest(t.Context(), "parity")+"\n"))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "instance-group.txt"), recorded.String())
}

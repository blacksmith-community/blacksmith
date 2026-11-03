package services

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

// storedManifestVault answers a read of any manifest path with one stored
// manifest.
type storedManifestVault struct {
	testutil.InstanceVault

	manifest string
}

func (v *storedManifestVault) Get(_ context.Context, _ string, out interface{}) (bool, error) {
	body, err := json.Marshal(map[string]string{"manifest": v.manifest})
	if err != nil {
		return false, err //nolint:wrapcheck // a test double
	}

	return true, json.Unmarshal(body, out) //nolint:wrapcheck // a test double
}

// TestInstanceDetailsManifestParity records the manifest the instance
// details read makes of each stored manifest text, as the JSON the endpoint
// would answer with, and the vault sources it lists. A text that does not
// parse is left out of the answer and only logged.
func TestInstanceDetailsManifestParity(t *testing.T) {
	t.Parallel()

	log := logger.Get().Named("test")

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		handler := NewHandler(Dependencies{Logger: log, Vault: &storedManifestVault{manifest: doc.Body}})
		resp := &instanceConfigResponse{InstanceID: "inst-1"}

		err := handler.loadManifestSecret(context.Background(), "inst-1", resp, log)

		body := "sources " + strings.Join(resp.VaultSources, ",") + "\n"
		if err != nil {
			body += "ERROR: " + err.Error() + "\n"
		}

		encoded, marshalErr := json.Marshal(resp)
		if marshalErr != nil {
			body += "JSON ERROR: " + marshalErr.Error() + "\n"
		} else {
			body += string(encoded) + "\n"
		}

		recorded.WriteString(parity.Section(doc.Name, body))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "instance-details-manifest.txt"), recorded.String())
}

package services

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/pkg/logger"
)

// TestManifestResponseParity records the JSON the manifest endpoints write
// for each manifest, and the instance group name they find in it.
func TestManifestResponseParity(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Dependencies{Logger: logger.Get().Named("test")})

	var responses, groups strings.Builder

	for _, doc := range parity.Corpus(t) {
		recorder := httptest.NewRecorder()
		handler.writeManifestFromText(recorder, logger.Get().Named("test"), doc.Body)
		responses.WriteString(parity.Section(doc.Name, recorder.Body.String()))

		name, err := firstInstanceGroupName(doc.Body)
		if err != nil {
			groups.WriteString(parity.Section(doc.Name, "ERROR: "+err.Error()+"\n"))

			continue
		}

		groups.WriteString(parity.Section(doc.Name, name+"\n"))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "manifest-response.txt"), responses.String())
	parity.Golden(t, filepath.Join("testdata", "parity", "first-instance-group.txt"), groups.String())
}

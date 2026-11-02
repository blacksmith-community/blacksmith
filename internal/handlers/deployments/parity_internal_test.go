package deployments

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/parity"
	"blacksmith/pkg/logger"
)

// manifestDirector answers GetDeployment with one manifest.
type manifestDirector struct {
	bosh.Director

	manifest string
}

func (d *manifestDirector) GetDeployment(context.Context, string) (*bosh.DeploymentDetail, error) {
	return &bosh.DeploymentDetail{Manifest: d.manifest}, nil
}

// TestReadManifestParity records the text and parsed form the deployment
// manifest endpoint answers with for each manifest.
func TestReadManifestParity(t *testing.T) {
	t.Parallel()

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Director: &manifestDirector{manifest: doc.Body}})

		got, err := handler.readManifest(context.Background(), "parity")
		if err != nil {
			recorded.WriteString(parity.Section(doc.Name, "ERROR: "+err.Error()+"\n"))

			continue
		}

		recorded.WriteString(parity.Section(doc.Name, parity.Dump(got)))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "read-manifest.dump"), recorded.String())
}

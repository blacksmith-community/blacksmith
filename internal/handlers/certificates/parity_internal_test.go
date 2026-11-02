package certificates

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/pkg/logger"
)

// TestExtractCertificatesFromManifestParity records the certificates the
// handler finds in each manifest, by path. The cert inventory's handwritten
// manifest carries real, throwaway public certificates, so the finds are not
// empty. The walk visits map keys in no fixed order, so the finds are sorted.
func TestExtractCertificatesFromManifestParity(t *testing.T) {
	t.Parallel()

	handwritten, err := os.ReadFile(filepath.Join("..", "..", "certinventory", "testdata", "manifests", "handwritten.yml"))
	if err != nil {
		t.Fatal(err)
	}

	docs := append(parity.Corpus(t), parity.Document{Name: "handwritten-certs", Body: string(handwritten)})
	handler := &Handler{logger: logger.Get().Named("test")}

	var recorded strings.Builder

	for _, doc := range docs {
		found := handler.extractCertificatesFromManifest(context.Background(), doc.Body)

		lines := make([]string, 0, len(found))
		for _, item := range found {
			lines = append(lines, strings.Join([]string{item.Name, item.Path, item.Details.Subject.CommonName, item.Details.Fingerprints.SHA256}, "\t"))
		}

		sort.Strings(lines)

		recorded.WriteString(parity.Section(doc.Name, strings.Join(lines, "\n")+"\n"))
	}

	parity.Golden(t, filepath.Join("testdata", "parity", "manifest-certificates.txt"), recorded.String())
}

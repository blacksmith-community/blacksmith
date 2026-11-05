package upgrade

import (
	"fmt"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// MergeStemcellOverlay sets the stemcell in an existing manifest. The
// stemcell it writes is:
//
//	---
//	stemcells:
//	- alias: default
//	  os: ubuntu-jammy
//	  version: <selected-version>
//
// The first stemcell entry is replaced by it, or the entry is added when
// the manifest has none. The overlay sets the stemcell directly and never
// merges, so no operator and no merge marker in the manifest takes effect.
// The manifest comes back from the director, and it holds the instance's
// nested request parameters as the literal (( )) text a tenant sent, which
// the render kept inert. That text has to stay exactly as it was written.
func MergeStemcellOverlay(manifestYAML string, stemcellOS, stemcellVersion string) (string, error) {
	// Parse the existing manifest
	var manifest map[string]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", fmt.Errorf("failed to parse the manifest for the stemcell overlay: %w", err)
	}

	if manifest == nil {
		manifest = map[string]interface{}{}
	}

	setStemcell(manifest, createStemcellOverlay(stemcellOS, stemcellVersion))

	// Marshal back to YAML
	result, err := yaml.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("failed to marshal the manifest after the stemcell overlay: %w", err)
	}

	return string(result), nil
}

// setStemcell writes the overlay's stemcell over the manifest's first
// stemcell. A first stemcell that is a map keeps its other keys, and any
// other first entry is replaced. A manifest with no stemcell list gets one.
func setStemcell(manifest, overlay map[string]interface{}) {
	stemcell, _ := overlay["stemcells"].([]interface{})[0].(map[string]interface{})

	existing, ok := manifest["stemcells"].([]interface{})
	if !ok || len(existing) == 0 {
		manifest["stemcells"] = overlay["stemcells"]

		return
	}

	first, ok := existing[0].(map[string]interface{})
	if !ok {
		existing[0] = stemcell

		return
	}

	for key, value := range stemcell {
		first[key] = value
	}
}

// createStemcellOverlay creates the stemcell overlay structure.
func createStemcellOverlay(os, version string) map[string]interface{} {
	stemcell := map[string]interface{}{
		"alias":   "default",
		"os":      os,
		"version": version,
	}

	stemcells := []interface{}{stemcell}

	return map[string]interface{}{
		"stemcells": stemcells,
	}
}

// GetCurrentStemcell extracts the current stemcell info from a manifest.
func GetCurrentStemcell(manifestYAML string) (os string, version string, err error) {
	var manifest map[string]interface{}

	err = yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse manifest: %w", err)
	}

	stemcells, ok := manifest["stemcells"].([]interface{})
	if !ok || len(stemcells) == 0 {
		return "", "", fmt.Errorf("no stemcells found in manifest")
	}

	// Get the first stemcell (usually the default one)
	firstStemcell, ok := stemcells[0].(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("invalid stemcell format in manifest")
	}

	os, _ = firstStemcell["os"].(string)
	version, _ = firstStemcell["version"].(string)

	if os == "" || version == "" {
		return "", "", fmt.Errorf("stemcell os or version is empty")
	}

	return os, version, nil
}

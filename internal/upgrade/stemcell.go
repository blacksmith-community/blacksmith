package upgrade

import (
	"fmt"

	manifestPkg "blacksmith/internal/manifest"

	"github.com/fivetwenty-io/graft/pkg/graft/merger"
	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// MergeStemcellOverlay merges a stemcell overlay into an existing manifest.
// The overlay format is:
//
//	---
//	stemcells:
//	- alias: default
//	  os: ubuntu-jammy
//	  version: <selected-version>
//
// The overlay merges and never evaluates an operator. The manifest comes
// back from the director, and it holds the instance's nested request
// parameters as the literal (( )) text a tenant sent, which the render
// kept inert. Evaluating it here would run that tenant's grab, file,
// vault, and other operators with the broker's environment and
// credentials. The overlay itself holds only a stemcell name and version,
// so it needs no operator.
func MergeStemcellOverlay(manifestYAML string, stemcellOS, stemcellVersion string) (string, error) {
	// Parse the existing manifest
	var manifest map[string]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", fmt.Errorf("failed to parse the manifest for the stemcell overlay: %w", err)
	}

	// Create the stemcell overlay
	overlay := createStemcellOverlay(stemcellOS, stemcellVersion)

	// The merge runs under the lock the manifest package holds around its
	// own renders.
	var tree map[string]interface{}

	manifestPkg.WithManifestLock(func() {
		tree, err = merger.Merge(manifest, overlay)
	})
	if err != nil {
		return "", fmt.Errorf("failed to merge the stemcell overlay: %w", err)
	}

	// Marshal back to YAML
	result, err := yaml.Marshal(tree)
	if err != nil {
		return "", fmt.Errorf("failed to marshal the manifest after the stemcell overlay: %w", err)
	}

	return string(result), nil
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

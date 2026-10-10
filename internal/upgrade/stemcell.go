package upgrade

import (
	"fmt"

	manifestPkg "blacksmith/internal/manifest"

	"github.com/geofffranks/spruce"
	"gopkg.in/yaml.v2"
)

// MergeStemcellOverlay merges a stemcell overlay into an existing manifest.
// The overlay format is:
//
//	---
//	stemcells:
//	- alias: default
//	  os: ubuntu-jammy
//	  version: <selected-version>
func MergeStemcellOverlay(manifestYAML string, stemcellOS, stemcellVersion string) (string, error) {
	// Parse the existing manifest
	var manifest map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	// Create the stemcell overlay
	overlay := createStemcellOverlay(stemcellOS, stemcellVersion)

	// Spruce keeps global state, so the merge and the evaluation run under
	// the lock the manifest package holds around its own spruce calls.
	var tree map[interface{}]interface{}

	manifestPkg.WithSpruceLock(func() {
		tree, err = mergeAndEvaluate(manifest, overlay)
	})
	if err != nil {
		return "", err
	}

	// Marshal back to YAML
	result, err := yaml.Marshal(tree)
	if err != nil {
		return "", fmt.Errorf("failed to marshal merged manifest: %w", err)
	}

	return string(result), nil
}

// mergeAndEvaluate merges the overlay into the manifest with spruce and
// runs the spruce evaluator on the result. The caller holds the spruce
// lock.
func mergeAndEvaluate(manifest, overlay map[interface{}]interface{}) (map[interface{}]interface{}, error) {
	merged, err := spruce.Merge(manifest, overlay)
	if err != nil {
		return nil, fmt.Errorf("failed to merge stemcell overlay: %w", err)
	}

	eval := &spruce.Evaluator{Tree: merged}

	err = eval.Run(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate spruce expressions: %w", err)
	}

	return eval.Tree, nil
}

// createStemcellOverlay creates the stemcell overlay structure.
func createStemcellOverlay(os, version string) map[interface{}]interface{} {
	stemcell := map[interface{}]interface{}{
		"alias":   "default",
		"os":      os,
		"version": version,
	}

	stemcells := []interface{}{stemcell}

	return map[interface{}]interface{}{
		"stemcells": stemcells,
	}
}

// GetCurrentStemcell extracts the current stemcell info from a manifest.
func GetCurrentStemcell(manifestYAML string) (os string, version string, err error) {
	var manifest map[interface{}]interface{}

	err = yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse manifest: %w", err)
	}

	stemcells, ok := manifest["stemcells"].([]interface{})
	if !ok || len(stemcells) == 0 {
		return "", "", fmt.Errorf("no stemcells found in manifest")
	}

	// Get the first stemcell (usually the default one)
	firstStemcell, ok := stemcells[0].(map[interface{}]interface{})
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

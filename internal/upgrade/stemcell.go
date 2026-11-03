package upgrade

import (
	"context"
	"fmt"

	manifestPkg "blacksmith/internal/manifest"

	"github.com/fivetwenty-io/graft/pkg/graft"
	_ "github.com/fivetwenty-io/graft/pkg/graft/operators" // registers the operators the evaluation runs
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
func MergeStemcellOverlay(manifestYAML string, stemcellOS, stemcellVersion string) (string, error) {
	// Parse the existing manifest
	var manifest map[string]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	// Create the stemcell overlay
	overlay := createStemcellOverlay(stemcellOS, stemcellVersion)

	// The merge and the evaluation run under the lock the manifest package
	// holds around its own renders.
	var tree interface{}

	manifestPkg.WithManifestLock(func() {
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

// mergeAndEvaluate merges the overlay into the manifest with graft and
// evaluates the operators in the result, on an engine of its own with no
// cache. The caller holds the manifest lock.
func mergeAndEvaluate(manifest, overlay map[string]interface{}) (interface{}, error) {
	engine, err := graft.NewEngine(graft.WithCacheDisabled())
	if err != nil {
		return nil, fmt.Errorf("failed to create the graft engine: %w", err)
	}

	merged, err := engine.Merge(context.Background(), graft.NewDocument(manifest), graft.NewDocument(overlay)).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to merge stemcell overlay: %w", err)
	}

	return merged.RawData(), nil
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

package upgrade

import (
	"fmt"

	"github.com/geofffranks/spruce"
	"gopkg.in/yaml.v2"
)

// MergeReleaseOverlay merges release name+version overlays into an existing manifest's
// `releases:` block. Spruce merges arrays of maps by the `name` key, so each overlay entry
// updates the version of the matching release (e.g. the forge release) and leaves every other
// release (bpm, routing, …) exactly as the instance currently has it. A no-op if no releases.
func MergeReleaseOverlay(manifestYAML string, releases []ReleaseTarget) (string, error) {
	if len(releases) == 0 {
		return manifestYAML, nil
	}

	var manifest map[interface{}]interface{}

	if err := yaml.Unmarshal([]byte(manifestYAML), &manifest); err != nil {
		return "", fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	overlay := createReleaseOverlay(releases)

	merged, err := spruce.Merge(manifest, overlay)
	if err != nil {
		return "", fmt.Errorf("failed to merge release overlay: %w", err)
	}

	eval := &spruce.Evaluator{Tree: merged}
	if err := eval.Run(nil, nil); err != nil {
		return "", fmt.Errorf("failed to evaluate spruce expressions: %w", err)
	}

	result, err := yaml.Marshal(eval.Tree)
	if err != nil {
		return "", fmt.Errorf("failed to marshal merged manifest: %w", err)
	}

	return string(result), nil
}

// createReleaseOverlay builds a `releases:` overlay from the target list.
func createReleaseOverlay(releases []ReleaseTarget) map[interface{}]interface{} {
	list := make([]interface{}, 0, len(releases))
	for _, r := range releases {
		list = append(list, map[interface{}]interface{}{
			"name":    r.Name,
			"version": r.Version,
		})
	}

	return map[interface{}]interface{}{"releases": list}
}

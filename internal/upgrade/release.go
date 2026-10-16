package upgrade

import (
	"fmt"

	"gopkg.in/yaml.v2"
)

// MergeReleaseOverlay sets the version of each target release that the
// manifest's releases block already holds, and returns the new manifest
// and the targets it applied. A target the manifest does not hold is left
// out, so the release is never added to a deployment that does not use
// it. A bumped release loses its url and sha1, which name the tarball of
// its old version, since the target version must already be on the
// director, and its stemcell and exported_from, which name the stemcells
// that old tarball was compiled for. When no target applies, the manifest
// comes back unchanged.
func MergeReleaseOverlay(manifestYAML string, targets []ReleaseTarget) (string, []ReleaseTarget, error) {
	if len(targets) == 0 {
		return manifestYAML, nil, nil
	}

	var manifest map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	releases, _ := manifest["releases"].([]interface{})
	applied := make([]ReleaseTarget, 0, len(targets))

	for _, target := range targets {
		if bumpRelease(releases, target) {
			applied = append(applied, target)
		}
	}

	if len(applied) == 0 {
		return manifestYAML, nil, nil
	}

	result, err := yaml.Marshal(manifest)
	if err != nil {
		return "", nil, fmt.Errorf("failed to marshal merged manifest: %w", err)
	}

	return string(result), applied, nil
}

// bumpRelease sets the target's version on the release of the same name
// in releases, drops the keys that describe that release's old tarball,
// and reports whether releases held it.
func bumpRelease(releases []interface{}, target ReleaseTarget) bool {
	for _, entry := range releases {
		release, ok := entry.(map[interface{}]interface{})
		if !ok || release["name"] != target.Name {
			continue
		}

		release["version"] = target.Version
		delete(release, "url")
		delete(release, "sha1")
		delete(release, "stemcell")
		delete(release, "exported_from")

		return true
	}

	return false
}

package websocket

import (
	"errors"
	"testing"

	"blacksmith/pkg/logger"
)

// TestBlacksmithManifestMetadataEntries pins the instance group name and
// the error the handler returns for each shape the first instance group
// entry can have.
func TestBlacksmithManifestMetadataEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		manifest string
		group    string
		err      error
	}{
		"a map with a name":  {manifest: "name: d\ninstance_groups:\n- name: \" ssh \"\n", group: "ssh"},
		"a map with no name": {manifest: "name: d\ninstance_groups:\n- instances: 1\n", err: errFirstInstanceGroupMissingName},
		"a string entry":     {manifest: "name: d\ninstance_groups:\n- ssh\n", err: errFirstInstanceGroupInvalidFormat},
		"a null entry":       {manifest: "name: d\ninstance_groups:\n- ~\n", err: errFirstInstanceGroupInvalidFormat},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			log := logger.Get().Named("test")
			handler := NewHandler(Dependencies{Logger: log})

			_, group, err := handler.extractBlacksmithManifestMetadata(tc.manifest, "fallback", log)
			if !errors.Is(err, tc.err) {
				t.Errorf("error %v, want %v", err, tc.err)
			}

			if tc.err == nil && group != tc.group {
				t.Errorf("instance group %q, want %q", group, tc.group)
			}
		})
	}
}

// TestExtractGroupNameEntries pins the name extractGroupName takes from
// each shape the first instance group entry can have.
func TestExtractGroupNameEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		entry interface{}
		want  string
	}{
		"a map with a name":           {entry: map[string]interface{}{"name": "ssh"}, want: "ssh"},
		"a map with no name":          {entry: map[string]interface{}{"instances": 1}, want: ""},
		"a name that is not a string": {entry: map[string]interface{}{"name": 3}, want: ""},
		"a string entry":              {entry: "ssh", want: ""},
		"a null entry":                {entry: nil, want: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			log := logger.Get().Named("test")
			handler := NewHandler(Dependencies{Logger: log})

			got := handler.extractGroupName(tc.entry, "entries", log)
			if got != tc.want {
				t.Errorf("instance group %q, want %q", got, tc.want)
			}
		})
	}
}

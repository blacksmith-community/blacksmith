package websocket

import (
	"testing"

	"blacksmith/pkg/logger"
)

// TestInstanceGroupFromManifestEntries pins the name the handler takes
// from the first instance group for each shape that entry can have.
func TestInstanceGroupFromManifestEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		manifest string
		want     string
	}{
		"a map with a name":           {manifest: "instance_groups:\n- name: \" rabbitmq \"\n", want: "rabbitmq"},
		"a map with no name":          {manifest: "instance_groups:\n- instances: 1\n", want: ""},
		"a name that is not a string": {manifest: "instance_groups:\n- name: 3\n", want: ""},
		"a string entry":              {manifest: "instance_groups:\n- rabbitmq\n", want: ""},
		"a null entry":                {manifest: "instance_groups:\n- ~\n", want: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler := NewHandler(Dependencies{Logger: logger.Get().Named("test"), Vault: &manifestVault{manifest: tc.manifest}})

			got := handler.extractInstanceGroupFromManifest(t.Context(), "entries")
			if got != tc.want {
				t.Errorf("instance group %q, want %q", got, tc.want)
			}
		})
	}
}

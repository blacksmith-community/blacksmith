package reconciler

import "testing"

// TestParseReleaseInfoEntries pins what the scanner records for each shape
// a release entry can have.
func TestParseReleaseInfoEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		entry interface{}
		want  string
	}{
		"a map with a name and version": {entry: map[string]interface{}{"name": "redis", "version": "1.0"}, want: "redis/1.0"},
		"a map with no name":            {entry: map[string]interface{}{"version": "1.0"}, want: ""},
		"a string entry":                {entry: "redis/1.0", want: ""},
		"a null entry":                  {entry: nil, want: ""},
	}

	scanner := &BoshScanner{}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := scanner.parseReleaseInfo(tc.entry); got != tc.want {
				t.Errorf("release %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseStemcellInfoEntries pins what the scanner records for each
// shape a stemcell entry can have.
func TestParseStemcellInfoEntries(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		entry interface{}
		want  string
	}{
		"a map with an os and version": {
			entry: map[string]interface{}{"alias": "default", "os": "ubuntu-noble", "version": "1.10"},
			want:  "default/ubuntu-noble/1.10",
		},
		"a map with no alias": {
			entry: map[string]interface{}{"os": "ubuntu-noble", "version": "1.10"},
			want:  "ubuntu-noble/1.10",
		},
		"a string entry": {entry: "ubuntu-noble/1.10", want: ""},
		"a null entry":   {entry: nil, want: ""},
	}

	scanner := &BoshScanner{}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := scanner.parseStemcellInfo(tc.entry); got != tc.want {
				t.Errorf("stemcell %q, want %q", got, tc.want)
			}
		})
	}
}

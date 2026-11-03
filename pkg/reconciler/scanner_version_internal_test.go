package reconciler

import "testing"

func TestGetVersionValueKeepsEveryDigit(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		value interface{}
		want  string
	}{
		"string":         {"1.10", "1.10"},
		"int":            {621, "621"},
		"float":          {1.2, "1.2"},
		"two decimals":   {1.25, "1.25"},
		"stemcell float": {621.125, "621.125"},
		"whole float":    {3.0, "3.0"},
		"not a version":  {true, ""},
	}

	scanner := &BoshScanner{}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := scanner.getVersionValue(map[string]interface{}{"version": tc.value}, "version")
			if got != tc.want {
				t.Errorf("getVersionValue(%v) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

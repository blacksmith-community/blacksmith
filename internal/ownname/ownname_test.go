package ownname_test

import (
	"os"
	"path/filepath"
	"testing"

	"blacksmith/internal/ownname"
)

const (
	prodEnvironment = "prod"
	builtName       = "prod-blacksmith"
)

func writeInstanceFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "deployment")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("writing the instance file: %v", err)
	}

	return path
}

func TestResolveNamesTheOwnDeployment(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "absent")

	cases := map[string]struct {
		file        string
		environment string
		want        string
		wantFound   bool
	}{
		"the instance file wins":          {file: writeInstanceFile(t, "lab-bs\n"), environment: prodEnvironment, want: "lab-bs", wantFound: true},
		"a padded file is trimmed":        {file: writeInstanceFile(t, "  lab-bs \n"), environment: prodEnvironment, want: "lab-bs", wantFound: true},
		"no file builds it from the env":  {file: missing, environment: prodEnvironment, want: builtName, wantFound: true},
		"an empty file builds it":         {file: writeInstanceFile(t, " \n"), environment: prodEnvironment, want: builtName, wantFound: true},
		"a padded environment is trimmed": {file: missing, environment: "  prod ", want: builtName, wantFound: true},
		"no file and no env names none":   {file: missing, environment: "", want: "", wantFound: false},
		"a blank env names none":          {file: missing, environment: "   ", want: "", wantFound: false},
		"a file alone is enough":          {file: writeInstanceFile(t, "x"), environment: "", want: "x", wantFound: true},
	}

	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, found := ownname.Resolve(scenario.file, scenario.environment)
			if got != scenario.want || found != scenario.wantFound {
				t.Errorf("Resolve = (%q, %v), want (%q, %v)", got, found, scenario.want, scenario.wantFound)
			}
		})
	}
}

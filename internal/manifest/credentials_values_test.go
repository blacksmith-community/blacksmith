package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
)

// bindCertificate is a multi-line credential, as a PEM certificate is.
const bindCertificate = "-----BEGIN CERTIFICATE-----\nZml4dHVyZS1vbmx5\n-----END CERTIFICATE-----\n"

// bindCredentials are the credentials the bind tests render, with nested
// maps, a list, an integer, booleans, a string y, and a multi-line value.
func bindCredentials() map[string]interface{} {
	return map[string]interface{}{
		"host":        "db.example.test",
		"port":        5432,
		"tls":         true,
		"replica":     "y",
		"hosts":       []interface{}{"10.0.0.1", "10.0.0.2"},
		"certificate": bindCertificate,
		"admin": map[string]interface{}{
			"username": "fixture-admin",
			"limits":   map[string]interface{}{"connections": 100, "readonly": false},
		},
	}
}

// TestExtractCredentialsKeepsTheBindValues reads the credentials of a
// manifest GenManifest rendered and checks the exact Go values a bind
// response carries, so a change of YAML reader cannot change a type or a
// value the client sees.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestExtractCredentialsKeepsTheBindValues(t *testing.T) {
	t.Setenv("CREDENTIALS", "")

	plan := services.Plan{ID: "bind-values", Manifest: map[string]interface{}{"credentials": bindCredentials()}}

	rendered, err := GenManifest(plan, "bind-values-instance")
	if err != nil {
		t.Fatalf("render: %s", err)
	}

	got, err := extractCredentialsFromManifest(rendered, &logger.NoOpLogger{})
	if err != nil {
		t.Fatalf("extract: %s", err)
	}

	assertSameCredentials(t, bindCredentials(), got)
}

// TestExtractCredentialsReadsPlainYAML11Scalars pins what a plain y and n in
// the credentials become. GenManifest quotes both, so only a manifest written
// another way holds them plain. They stay strings, where yaml.v2 read them as
// booleans.
func TestExtractCredentialsReadsPlainYAML11Scalars(t *testing.T) {
	t.Parallel()

	manifest := "credentials:\n  replica: y\n  primary: n\n  enabled: yes\n  port: 5432\n  certificate: |\n" +
		"    -----BEGIN CERTIFICATE-----\n    Zml4dHVyZS1vbmx5\n    -----END CERTIFICATE-----\n"

	got, err := extractCredentialsFromManifest(manifest, &logger.NoOpLogger{})
	if err != nil {
		t.Fatalf("extract: %s", err)
	}

	want := map[string]interface{}{
		"replica":     "y",
		"primary":     "n",
		"enabled":     true,
		"port":        5432,
		"certificate": bindCertificate,
	}

	assertSameCredentials(t, want, got)
}

// TestExtractCredentialsRefusesWhatIsNotAMap checks that a manifest whose
// credentials are missing, null, or not a map gives no credentials and the
// error for its shape. Missing or null credentials, and a root that is not a
// map, give ErrCredentialsFieldNotFound, a string gives ErrCredentialsNotMap,
// and any other value gives ErrCredentialsWrongType.
func TestExtractCredentialsRefusesWhatIsNotAMap(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		manifest string
		want     error
	}{
		"missing":     {"name: fixture\n", ErrCredentialsFieldNotFound},
		"null":        {"credentials: ~\n", ErrCredentialsFieldNotFound},
		"list root":   {"- credentials\n", ErrCredentialsFieldNotFound},
		"scalar root": {"credentials\n", ErrCredentialsFieldNotFound},
		"empty":       {"", ErrCredentialsFieldNotFound},
		"list":        {"credentials: [a, b]\n", ErrCredentialsWrongType},
		"integer":     {"credentials: 5432\n", ErrCredentialsWrongType},
		"string":      {"credentials: fixture-string\n", ErrCredentialsNotMap},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := extractCredentialsFromManifest(tc.manifest, &logger.NoOpLogger{})
			if err == nil || got != nil {
				t.Fatalf("expected an error and no credentials, got an error %v and credentials of type %T", err, got)
			}

			if strings.Contains(err.Error(), "fixture") {
				t.Error("expected the error to quote nothing from the manifest")
			}

			if !errors.Is(err, tc.want) {
				t.Errorf("expected %v, got: %v", tc.want, err)
			}
		})
	}
}

// TestExtractCredentialsKeepsAnEmptyMap checks that empty credentials give
// an empty map and no error.
func TestExtractCredentialsKeepsAnEmptyMap(t *testing.T) {
	t.Parallel()

	got, err := extractCredentialsFromManifest("credentials: {}\n", &logger.NoOpLogger{})
	if err != nil {
		t.Fatalf("extract: %s", err)
	}

	assertSameCredentials(t, map[string]interface{}{}, got)
}

// assertSameCredentials fails the test when got is not exactly want. It
// names each path whose Go type or value differs, with the two types, and
// never prints a value.
func assertSameCredentials(t *testing.T, want map[string]interface{}, got interface{}) {
	t.Helper()

	if reflect.DeepEqual(interface{}(want), got) {
		return
	}

	wantLeaves, gotLeaves := dumpedLeaves(parity.Dump(want)), dumpedLeaves(parity.Dump(got))

	paths := map[string]bool{}
	for path := range wantLeaves {
		paths[path] = true
	}

	for path := range gotLeaves {
		paths[path] = true
	}

	var differ []string

	for path := range paths {
		if wantLeaves[path] != gotLeaves[path] {
			differ = append(differ, path+": want "+wantLeaves[path].typeName+", got "+gotLeaves[path].typeName)
		}
	}

	sort.Strings(differ)
	t.Errorf("credentials of type %T differ at:\n%s", got, strings.Join(differ, "\n"))
}

// leaf is the Go type and value parity.Dump records at one path.
type leaf struct{ typeName, value string }

func dumpedLeaves(dump string) map[string]leaf {
	leaves := map[string]leaf{}

	for _, line := range strings.Split(strings.TrimSuffix(dump, "\n"), "\n") {
		if fields := strings.SplitN(line, "\t", 3); len(fields) == 3 {
			leaves[fields[0]] = leaf{typeName: fields[1], value: fields[2]}
		}
	}

	return leaves
}

// TestBindCredentialsKeepFloatsAsNumbers renders credentials that grab a
// whole float and a fractional one, extracts them, and encodes them as a
// bind response does. Both must reach the JSON as numbers, never as
// strings.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestBindCredentialsKeepFloatsAsNumbers(t *testing.T) {
	t.Setenv("CREDENTIALS", "")

	plan := services.Plan{ID: "bind-floats", Manifest: map[string]interface{}{
		"meta": map[string]interface{}{"whole": 2000000.0, "fraction": 2.5},
		"credentials": map[string]interface{}{
			"whole":    "(( grab meta.whole ))",
			"fraction": "(( grab meta.fraction ))",
		},
	}}

	rendered, err := GenManifest(plan, "bind-floats-instance")
	if err != nil {
		t.Fatalf("render: %s", err)
	}

	credentials, err := extractCredentialsFromManifest(rendered, &logger.NoOpLogger{})
	if err != nil {
		t.Fatalf("extract: %s", err)
	}

	encoded, err := json.Marshal(credentials)
	if err != nil {
		t.Fatalf("encode: %s", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()

	var decoded map[string]interface{}

	err = decoder.Decode(&decoded)
	if err != nil {
		t.Fatalf("decode: %s", err)
	}

	for _, key := range []string{"whole", "fraction"} {
		if _, isNumber := decoded[key].(json.Number); !isNumber {
			t.Errorf("credentials.%s reaches the JSON as %T, want a number", key, decoded[key])
		}
	}
}

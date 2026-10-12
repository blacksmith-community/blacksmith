package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/config"
)

// boshEnvironment returns the BOSH CLI variables the broker no longer
// exports, because nothing in it reads them.
func boshEnvironment() []string {
	return []string{"BOSH_ENVIRONMENT", "BOSH_CLIENT", "BOSH_CLIENT_SECRET", "BOSH_CA_CERT"}
}

// clearEnvironment unsets each variable for the rest of the test, and puts
// back the value it had, if any, when the test ends.
func clearEnvironment(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		t.Setenv(name, "")

		err := os.Unsetenv(name)
		if err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
}

// keepEnvironment puts each variable back as it was when the test ends, for
// the variables the configuration loader still exports.
func keepEnvironment(t *testing.T, names ...string) {
	t.Helper()

	for _, name := range names {
		value, set := os.LookupEnv(name)
		t.Setenv(name, value)

		if !set {
			err := os.Unsetenv(name)
			if err != nil {
				t.Fatalf("unset %s: %v", name, err)
			}
		}
	}
}

// Loading the configuration and booting the BOSH layer put none of the
// director's address, credentials, or CA into the environment, where any
// child process the broker starts would inherit the client secret.
//
//nolint:paralleltest // the test reads and restores the process environment
func TestBOSHCredentialsStayOutOfTheEnvironment(t *testing.T) {
	clearEnvironment(t, boshEnvironment()...)
	keepEnvironment(t, "BOSH_NETWORK", "VAULT_ADDR", "VAULT_TOKEN")

	director := boshtest.New(t, nil)

	raw := "vault:\n  address: https://vault.example.com\nbosh:\n  address: " + director.URL +
		"\n  username: " + bootCredential + "\n  password: bosh-secret\n  cacert: |\n" + indent(director.CACert, "    ")
	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	pool, batch, err := initializeBOSH(&cfg, newShutdownLog())
	if err != nil || pool == nil || batch == nil {
		t.Fatalf("expected the BOSH layer to boot with the configured director, got %v", err)
	}

	t.Cleanup(pool.Stop)
	t.Cleanup(batch.Stop)

	for _, name := range boshEnvironment() {
		if _, set := os.LookupEnv(name); set {
			t.Errorf("booting set %s, want it left unset", name)
		}
	}
}

// bootSecret is the bosh.password of the bosh blocks the boot tests build.
const bootSecret = "bosh-secret"

// boshAllowHTTPSetting is how a boot error names the plain HTTP override,
// under both the broker's key and the BOSH release's property.
const boshAllowHTTPSetting = "bosh.allow_http (the release's bosh.allow_http)"

// runServiceStopDeadline is how long a boot on a refused bosh block may take
// to return. Such a boot sends no request, so it returns at once, and the
// deadline sits well below the wait for an unreachable Vault.
const runServiceStopDeadline = 10 * time.Second

// errBootPanicked reports a boot that panicked rather than returning.
var errBootPanicked = errors.New("the boot panicked")

// boshCASetting is how a boot error names the CA setting, under both the
// broker's key and the BOSH release's property.
const boshCASetting = "bosh.cacert (the release's bosh.ca_cert)"

// indent puts prefix before every line of text, for a YAML block scalar.
func indent(text, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimSuffix(text, "\n"), "\n", "\n"+prefix) + "\n"
}

// A bosh block the director client cannot be built from stops the boot with
// an error that names the setting, rather than booting a broker that fails
// every director call.
func TestBootStopsOnAnInvalidBOSHBlock(t *testing.T) {
	t.Parallel()

	cfg := config.Config{BOSH: config.BOSHConfig{
		Address:  "https://127.0.0.1:25555",
		Username: bootCredential,
		Password: bootSecret,
	}}

	pool, batch, err := initializeBOSH(&cfg, newShutdownLog())
	if !errors.Is(err, bosh.ErrDirectorCACertMissing) {
		t.Fatalf("expected the boot to stop on the missing CA, got %v", err)
	}

	if !strings.Contains(err.Error(), boshCASetting) {
		t.Errorf("expected the error to name %s, got: %s", boshCASetting, err.Error())
	}

	if pool != nil || batch != nil {
		t.Errorf("expected no directors from a refused bosh block, got %v and %v", pool, batch)
	}
}

// runService returns the error of a refused bosh block, so main exits with
// exitCodeError rather than serving. The error has to pass through both
// initializeCore and runService. Were initializeCore to drop it, the boot
// would go on to wait for the Vault below, which nothing answers, and the
// wait outlasts the deadline here. Were runService to drop it, the broker
// setup would get no configuration and panic, and the panic is reported
// as such. No request leaves the process: the boot stops before Vault is
// contacted, and the director address is never dialled.
//
//nolint:paralleltest // loading the configuration exports variables into the process environment
func TestRunServiceStopsOnAnInvalidBOSHBlock(t *testing.T) {
	tests := []struct {
		name    string
		bosh    string
		want    error
		setting string
	}{
		{name: "no CA", bosh: "  address: https://127.0.0.1:1\n",
			want: bosh.ErrDirectorCACertMissing, setting: boshCASetting},
		{name: "an http address that is not allowed",
			bosh: "  address: http://127.0.0.1:1\n  cacert: |\n" + indent(boshtest.New(t, nil).CACert, "    "),
			want: bosh.ErrDirectorPlainHTTP, setting: boshAllowHTTPSetting},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			keepEnvironment(t, "BOSH_NETWORK", "VAULT_ADDR", "VAULT_TOKEN")

			raw := "broker:\n  bind_ip: 127.0.0.1\n  port: \"0\"\n" +
				"vault:\n  address: https://127.0.0.1:1\n" +
				"bosh:\n  username: " + bootCredential + "\n  password: bosh-secret\n" + testCase.bosh

			err := runServiceUntilItStops(t, raw)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("expected runService to return %v, got: %v", testCase.want, err)
			}

			if !strings.Contains(err.Error(), testCase.setting) {
				t.Errorf("expected the error to name %s, got: %s", testCase.setting, err.Error())
			}
		})
	}
}

// runServiceUntilItStops writes raw as the configuration file, runs
// runService on it, and returns what runService returned, or the panic it
// raised as an error. The test fails when runService has not returned
// within runServiceStopDeadline.
func runServiceUntilItStops(t *testing.T, raw string) error {
	t.Helper()

	path := filepath.Join(t.TempDir(), "blacksmith.yml")

	err := os.WriteFile(path, []byte(raw), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	stopped := make(chan error, 1)

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				stopped <- fmt.Errorf("%w: %v", errBootPanicked, recovered)
			}
		}()

		stopped <- runService(path, BuildInfo{}, newShutdownLog())
	}()

	select {
	case err = <-stopped:
		return err
	case <-time.After(runServiceStopDeadline):
		t.Fatalf("runService did not stop within %s on a refused bosh block", runServiceStopDeadline)

		return nil
	}
}

// An http:// bosh.address stops the boot unless bosh.allow_http is set,
// and the error names both the address and the override.
func TestBootRefusesAPlainHTTPDirector(t *testing.T) {
	t.Parallel()

	cfg := config.Config{BOSH: config.BOSHConfig{
		Address:  "http://127.0.0.1:25555",
		Username: bootCredential,
		Password: bootSecret,
		CACert:   boshtest.New(t, nil).CACert,
	}}

	pool, batch, err := initializeBOSH(&cfg, newShutdownLog())
	if !errors.Is(err, bosh.ErrDirectorPlainHTTP) {
		t.Fatalf("expected the boot to stop on the http address, got %v", err)
	}

	for _, want := range []string{"bosh.address", boshAllowHTTPSetting} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to name %s, got: %s", want, err.Error())
		}
	}

	if pool != nil || batch != nil {
		t.Errorf("expected no directors from a refused bosh block, got %v and %v", pool, batch)
	}
}

// With bosh.allow_http set, an http:// director boots with both directors
// and one warning that the credentials go over plain HTTP. The setting
// makes no difference to an https:// director, which boots with no warning.
func TestBootAllowsAPlainHTTPDirectorWithAWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		address  string
		warnings int
	}{
		{name: "http", address: "http://127.0.0.1:25555", warnings: 1},
		{name: "https", address: "https://127.0.0.1:25555", warnings: 0},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Config{BOSH: config.BOSHConfig{
				Address:   testCase.address,
				Username:  bootCredential,
				Password:  bootSecret,
				CACert:    boshtest.New(t, nil).CACert,
				AllowHTTP: true,
			}}

			log := newShutdownLog()

			pool, batch, err := initializeBOSH(&cfg, log)
			if err != nil {
				t.Fatalf("expected the broker to boot, got %v", err)
			}

			if pool == nil || batch == nil {
				t.Fatalf("expected both directors, got %v and %v", pool, batch)
			}

			t.Cleanup(pool.Stop)
			t.Cleanup(batch.Stop)

			if got := strings.Count(log.output(), "go over plain HTTP"); got != testCase.warnings {
				t.Errorf("expected %d plain HTTP warnings, got %d in:\n%s", testCase.warnings, got, log.output())
			}

			if testCase.warnings > 0 && !strings.Contains(log.output(), testCase.address) {
				t.Errorf("expected the warning to name %s, got:\n%s", testCase.address, log.output())
			}
		})
	}
}

// A director that does not answer is not a configuration error. The broker
// boots with both directors, and the first call explains the failure.
func TestBootContinuesWhenTheDirectorIsUnreachable(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	address := listener.Addr().String()

	err = listener.Close()
	if err != nil {
		t.Fatalf("close the listener: %v", err)
	}

	cfg := config.Config{BOSH: config.BOSHConfig{
		Address:  "https://" + address,
		Username: bootCredential,
		Password: bootSecret,
		CACert:   boshtest.New(t, nil).CACert,
	}}

	pool, batch, err := initializeBOSH(&cfg, newShutdownLog())
	if err != nil {
		t.Fatalf("expected the broker to boot with an unreachable director, got %v", err)
	}

	if pool == nil || batch == nil {
		t.Fatalf("expected both directors, got %v and %v", pool, batch)
	}

	t.Cleanup(pool.Stop)
	t.Cleanup(batch.Stop)
}

package upgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
)

const (
	testCommandKey   = "configured-key-value"
	testCommandWrong = "some-other-key-value"
)

func commandsConfig(enabled, root *bool, key string) config.CommandsConfig {
	return config.CommandsConfig{
		Operation:  config.CommandSwitch{On: enabled},
		Root:       root,
		Credential: config.CommandCredential{Key: key},
	}
}

func authzRequest(header *string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/b/commands", nil)
	if header != nil {
		r.Header.Set(CommandKeyHeader, *header)
	}

	return r
}

func strPtr(s string) *string { return &s }

// wantAPIError fails unless err is an *apierr.Error with the status and code,
// and never prints the secrets the test configured.
func wantAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()

	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, want an *apierr.Error with code %s", err, code)
	}

	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("got status %d code %s, want status %d code %s", apiErr.Status, apiErr.Code, status, code)
	}
}

func TestCommandKeyHeaderName(t *testing.T) {
	t.Parallel()

	if CommandKeyHeader != "X-Blacksmith-Command-Key" {
		t.Fatalf("CommandKeyHeader = %q", CommandKeyHeader)
	}
}

func TestAuthorizeRequest(t *testing.T) {
	t.Parallel()

	table := []struct {
		name   string
		cfg    config.CommandsConfig
		runAs  string
		header *string
		status int
		code   string
	}{
		{name: "vcap, feature on by default, no credential", cfg: commandsConfig(nil, nil, ""), runAs: "vcap"},
		{name: "vcap, enabled false", cfg: commandsConfig(boolPtr(false), nil, ""), runAs: "vcap", status: 403, code: "commands_disabled"},
		{name: "root, enabled false", cfg: commandsConfig(boolPtr(false), boolPtr(true), ""), runAs: "root", status: 403, code: "commands_disabled"},
		{name: "root, allow_root unset", cfg: commandsConfig(nil, nil, ""), runAs: "root", status: 403, code: "root_disabled"},
		{name: "root, allow_root false", cfg: commandsConfig(nil, boolPtr(false), ""), runAs: "root", status: 403, code: "root_disabled"},
		{name: "vcap, allow_root unset", cfg: commandsConfig(nil, nil, ""), runAs: "vcap"},
		{name: "root, allow_root true", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "root"},
		{name: "empty user", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "", status: 400, code: "invalid_run_user"},
		{name: "Root cased", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "Root", status: 400, code: "invalid_run_user"},
		{name: "padded vcap", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: " vcap", status: 400, code: "invalid_run_user"},
		{name: "VCAP cased", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "VCAP", status: 400, code: "invalid_run_user"},
		{name: "unknown user admin", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "admin", status: 400, code: "invalid_run_user"},
		{name: "key configured, header missing", cfg: commandsConfig(nil, nil, testCommandKey), runAs: "vcap", status: 401, code: "command_credential_required"},
		{name: "key configured, header empty", cfg: commandsConfig(nil, nil, testCommandKey), runAs: "vcap", header: strPtr(""), status: 401, code: "command_credential_required"},
		{name: "key configured, header wrong", cfg: commandsConfig(nil, nil, testCommandKey), runAs: "vcap", header: strPtr(testCommandWrong), status: 401, code: "command_credential_required"},
		{name: "key configured, header matches", cfg: commandsConfig(nil, nil, testCommandKey), runAs: "vcap", header: strPtr(testCommandKey)},
		{name: "root allowed, key matches", cfg: commandsConfig(nil, boolPtr(true), testCommandKey), runAs: "root", header: strPtr(testCommandKey)},
	}

	for _, row := range table {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			authz := NewConfigAuthorizer(func() config.CommandsConfig { return row.cfg })
			err := authz.AuthorizeRequest(context.Background(), authzRequest(row.header), row.runAs)

			if row.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: code check failed (%T)", err)
				}

				return
			}

			wantAPIError(t, err, row.status, row.code)
		})
	}
}

func TestAuthorizeStored(t *testing.T) {
	t.Parallel()

	table := []struct {
		name  string
		cfg   config.CommandsConfig
		runAs string
		code  string
	}{
		{name: "root, allow_root unset", cfg: commandsConfig(nil, nil, ""), runAs: "root", code: "root_disabled"},
		{name: "root, allow_root false", cfg: commandsConfig(nil, boolPtr(false), ""), runAs: "root", code: "root_disabled"},
		{name: "root, allow_root true", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "root"},
		{name: "vcap, allow_root unset", cfg: commandsConfig(nil, nil, ""), runAs: "vcap"},
		{name: "vcap, enabled false", cfg: commandsConfig(boolPtr(false), nil, ""), runAs: "vcap", code: "commands_disabled"},
		{name: "root, enabled false", cfg: commandsConfig(boolPtr(false), boolPtr(true), ""), runAs: "root", code: "commands_disabled"},
		{name: "vcap, credential configured is not checked", cfg: commandsConfig(nil, nil, testCommandKey), runAs: "vcap"},
		{name: "empty user", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "", code: "invalid_run_user"},
		{name: "Root cased", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "Root", code: "invalid_run_user"},
		{name: "padded vcap", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: " vcap", code: "invalid_run_user"},
		{name: "VCAP cased", cfg: commandsConfig(nil, boolPtr(true), ""), runAs: "VCAP", code: "invalid_run_user"},
	}

	for _, row := range table {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			authz := NewConfigAuthorizer(func() config.CommandsConfig { return row.cfg })
			err := authz.AuthorizeStored(context.Background(), CommandTaskSpec{RunAs: row.runAs})

			if row.code == "" {
				if err != nil {
					t.Fatalf("unexpected error (%T)", err)
				}

				return
			}

			status := http.StatusForbidden
			if row.code == "invalid_run_user" {
				status = http.StatusBadRequest
			}

			wantAPIError(t, err, status, row.code)
		})
	}
}

func TestAuthorizeRead(t *testing.T) {
	t.Parallel()

	table := []struct {
		name   string
		cfg    config.CommandsConfig
		header *string
		status int
		code   string
	}{
		// Ruling 16, past root output stays readable after allow_root is off.
		{name: "allow_root unset", cfg: commandsConfig(nil, nil, "")},
		{name: "allow_root false", cfg: commandsConfig(nil, boolPtr(false), "")},
		{name: "enabled false", cfg: commandsConfig(boolPtr(false), boolPtr(true), ""), status: 403, code: "commands_disabled"},
		{name: "key configured, header missing", cfg: commandsConfig(nil, nil, testCommandKey), status: 401, code: "command_credential_required"},
		{name: "key configured, header wrong", cfg: commandsConfig(nil, nil, testCommandKey), header: strPtr(testCommandWrong), status: 401, code: "command_credential_required"},
		{name: "key configured, header matches", cfg: commandsConfig(nil, nil, testCommandKey), header: strPtr(testCommandKey)},
	}

	for _, row := range table {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			authz := NewConfigAuthorizer(func() config.CommandsConfig { return row.cfg })
			err := authz.AuthorizeRead(context.Background(), authzRequest(row.header))

			if row.code == "" {
				if err != nil {
					t.Fatalf("unexpected error (%T)", err)
				}

				return
			}

			wantAPIError(t, err, row.status, row.code)
		})
	}
}

func TestAuthorizerReadsConfigOnEveryCall(t *testing.T) {
	t.Parallel()

	cfg := commandsConfig(nil, boolPtr(true), "")
	authz := NewConfigAuthorizer(func() config.CommandsConfig { return cfg })
	ctx := context.Background()
	stored := CommandTaskSpec{RunAs: "root"}

	err := authz.AuthorizeStored(ctx, stored)
	if err != nil {
		t.Fatalf("first call: unexpected error (%T)", err)
	}

	cfg = commandsConfig(nil, boolPtr(false), "")

	wantAPIError(t, authz.AuthorizeStored(ctx, stored), 403, "root_disabled")

	cfg = commandsConfig(boolPtr(false), boolPtr(true), "")

	wantAPIError(t, authz.AuthorizeRead(ctx, authzRequest(nil)), 403, "commands_disabled")
}

func TestNilSwitchesFailClosed(t *testing.T) {
	t.Parallel()

	authz := NewConfigAuthorizer(nil)
	ctx := context.Background()

	wantAPIError(t, authz.AuthorizeRequest(ctx, authzRequest(nil), "vcap"), 403, "commands_disabled")
	wantAPIError(t, authz.AuthorizeRead(ctx, authzRequest(nil)), 403, "commands_disabled")
	wantAPIError(t, authz.AuthorizeStored(ctx, CommandTaskSpec{RunAs: "vcap"}), 403, "commands_disabled")
}

func TestAuthorizerErrorsNeverCarrySecrets(t *testing.T) {
	t.Parallel()

	authz := NewConfigAuthorizer(func() config.CommandsConfig { return commandsConfig(nil, nil, testCommandKey) })
	ctx := context.Background()

	cases := map[string]error{
		"request, wrong header":   authz.AuthorizeRequest(ctx, authzRequest(strPtr(testCommandWrong)), "vcap"),
		"request, missing header": authz.AuthorizeRequest(ctx, authzRequest(nil), "vcap"),
		"request, empty header":   authz.AuthorizeRequest(ctx, authzRequest(strPtr("")), "vcap"),
		"read, wrong header":      authz.AuthorizeRead(ctx, authzRequest(strPtr(testCommandWrong))),
		"read, empty header":      authz.AuthorizeRead(ctx, authzRequest(strPtr(""))),
		"read, missing header":    authz.AuthorizeRead(ctx, authzRequest(nil)),
	}

	for name, err := range cases {
		if err == nil {
			t.Fatalf("%s: expected a refusal", name)
		}

		for _, secret := range []string{testCommandKey, testCommandWrong} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s: error message carries a credential", name)
			}
		}
	}
}

func TestLengthDifferingKeysAreRefused(t *testing.T) {
	t.Parallel()

	authz := NewConfigAuthorizer(func() config.CommandsConfig { return commandsConfig(nil, nil, testCommandKey) })

	for _, header := range []string{testCommandKey + "x", testCommandKey[:len(testCommandKey)-1]} {
		wantAPIError(t, authz.AuthorizeRead(context.Background(), authzRequest(&header)), 401, "command_credential_required")
	}
}

func TestZeroValueAuthorizerFailsClosed(t *testing.T) {
	t.Parallel()

	var authz ConfigAuthorizer

	ctx := context.Background()

	wantAPIError(t, authz.AuthorizeRequest(ctx, authzRequest(nil), "vcap"), 403, "commands_disabled")
	wantAPIError(t, authz.AuthorizeRead(ctx, authzRequest(nil)), 403, "commands_disabled")
	wantAPIError(t, authz.AuthorizeStored(ctx, CommandTaskSpec{RunAs: "vcap"}), 403, "commands_disabled")

	var none *ConfigAuthorizer

	wantAPIError(t, none.AuthorizeStored(ctx, CommandTaskSpec{RunAs: "vcap"}), 403, "commands_disabled")
}

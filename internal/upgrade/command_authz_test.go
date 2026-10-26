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
		t.Fatalf("error is %v, want an *apierr.Error with code %s", err, code)
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

			wantAPIError(t, err, 403, row.code)
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

func TestAuthorizerErrorsNeverCarrySecrets(t *testing.T) {
	t.Parallel()

	authz := NewConfigAuthorizer(func() config.CommandsConfig { return commandsConfig(nil, nil, testCommandKey) })

	err := authz.AuthorizeRequest(context.Background(), authzRequest(strPtr(testCommandWrong)), "vcap")
	if err == nil {
		t.Fatal("expected a refusal")
	}

	for _, secret := range []string{testCommandKey, testCommandWrong} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error message carries a credential")
		}
	}
}

package certinventory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
)

// fakeVault stores documents as JSON so a round trip behaves like Vault.
type fakeVault struct {
	interfaces.Vault

	docs map[string][]byte
	puts []string
}

func newFakeVault() *fakeVault { return &fakeVault{docs: map[string][]byte{}} }

func (f *fakeVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	raw, ok := f.docs[path]
	if !ok {
		return false, nil
	}

	return true, json.Unmarshal(raw, result)
}

func (f *fakeVault) Put(_ context.Context, path string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	f.docs[path] = raw
	f.puts = append(f.puts, path)

	return nil
}

func TestSettingsValidate(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name   string
		mutate func(*Settings)
		want   string // empty means valid
	}{
		{"defaults", func(*Settings) {}, ""},
		{"critical equals warning", func(s *Settings) { s.CriticalDays = 30 }, "critical_days must be below warning_days"},
		{"interval 4", func(s *Settings) { s.ScanIntervalMinutes = 4 }, "scan_interval_minutes"},
		{"interval 5", func(s *Settings) { s.ScanIntervalMinutes = 5 }, ""},
		{"concurrency 0", func(s *Settings) { s.ScanConcurrency = 0 }, "scan_concurrency"},
		{"concurrency 17", func(s *Settings) { s.ScanConcurrency = 17 }, "scan_concurrency"},
		{"concurrency 16", func(s *Settings) { s.ScanConcurrency = 16 }, ""},
		{"ttl 4", func(s *Settings) { s.PreviewTTLMinutes = 4 }, "preview_ttl_minutes"},
		{"ttl 61", func(s *Settings) { s.PreviewTTLMinutes = 61 }, "preview_ttl_minutes"},
		{"timeout 0", func(s *Settings) { s.InstanceScanTimeoutSeconds = 0 }, "instance_scan_timeout_seconds"},
		{"warning 0", func(s *Settings) { s.WarningDays = 0 }, "warning_days"},
	}

	for _, row := range rows {
		s := DefaultSettings()
		row.mutate(&s)
		err := s.Validate()

		if row.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", row.name, err)
			}

			continue
		}

		var ae *apierr.Error
		if !errors.As(err, &ae) {
			t.Errorf("%s: error %v is not an apierr.Error", row.name, err)

			continue
		}

		if ae.Status != http.StatusBadRequest || ae.Code != apierr.CodeInvalidSettings {
			t.Errorf("%s: got %d %s", row.name, ae.Status, ae.Code)
		}

		if !strings.Contains(ae.Message, row.want) {
			t.Errorf("%s: message %q lacks %q", row.name, ae.Message, row.want)
		}
	}
}

func TestSettingsStoreLoadDefaultsAndSave(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v := newFakeVault()
	store := NewSettingsStore(v)

	got, err := store.Load(ctx)
	if err != nil || got != DefaultSettings() {
		t.Fatalf("absent path: got %+v, %v", got, err)
	}

	v.docs[SettingsVaultPath] = []byte(`{"warning_days": 45}`)

	got, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}

	want := DefaultSettings()
	want.WarningDays = 45

	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	if store.Current() != want {
		t.Fatalf("Current = %+v", store.Current())
	}

	saved := DefaultSettings()
	saved.ScanConcurrency = 8

	if err := store.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}

	if len(v.puts) != 1 || v.puts[0] != "certificate-settings" {
		t.Fatalf("puts = %v", v.puts)
	}

	if store.Current() != saved {
		t.Fatalf("Current after save = %+v", store.Current())
	}

	bad := DefaultSettings()
	bad.CriticalDays = 99

	if err := store.Save(ctx, bad); err == nil || len(v.puts) != 1 {
		t.Fatalf("invalid save went through: %v, puts %v", err, v.puts)
	}
}

func TestSettingsStoreKeepsSavedFalse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v := newFakeVault()

	s := DefaultSettings()
	s.ProbeEnabled = false
	s.PauseOnFailure = false

	if err := NewSettingsStore(v).Save(ctx, s); err != nil {
		t.Fatal(err)
	}

	got, err := NewSettingsStore(v).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got.ProbeEnabled || got.PauseOnFailure {
		t.Fatalf("saved false came back true: %+v", got)
	}
}

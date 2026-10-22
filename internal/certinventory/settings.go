package certinventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
)

// SettingsVaultPath is where the runtime settings live in Vault.
const SettingsVaultPath = "certificate-settings"

// Settings are the runtime settings the UI edits.
type Settings struct {
	WarningDays                int  `json:"warning_days"`
	CriticalDays               int  `json:"critical_days"`
	ScanIntervalMinutes        int  `json:"scan_interval_minutes"`
	ScanConcurrency            int  `json:"scan_concurrency"`
	InstanceScanTimeoutSeconds int  `json:"instance_scan_timeout_seconds"`
	ProbeEnabled               bool `json:"probe_enabled"`
	PauseOnFailure             bool `json:"pause_on_failure"`
	PreviewTTLMinutes          int  `json:"preview_ttl_minutes"`
}

// DefaultSettings returns the settings used until an operator saves others.
func DefaultSettings() Settings {
	return Settings{
		WarningDays:                30,
		CriticalDays:               7,
		ScanIntervalMinutes:        60,
		ScanConcurrency:            4,
		InstanceScanTimeoutSeconds: 60,
		ProbeEnabled:               true,
		PauseOnFailure:             true,
		PreviewTTLMinutes:          15,
	}
}

func invalidSettings(format string, args ...interface{}) error {
	return apierr.New(http.StatusBadRequest, apierr.CodeInvalidSettings, fmt.Sprintf(format, args...))
}

// Validate checks every field and names the first one that fails.
func (s Settings) Validate() error {
	switch {
	case s.WarningDays < 1:
		return invalidSettings("warning_days must be at least 1")
	case s.CriticalDays < 1:
		return invalidSettings("critical_days must be at least 1")
	case s.CriticalDays >= s.WarningDays:
		return invalidSettings("critical_days must be below warning_days")
	case s.ScanIntervalMinutes < 5:
		return invalidSettings("scan_interval_minutes must be at least 5")
	case s.ScanConcurrency < 1 || s.ScanConcurrency > 16:
		return invalidSettings("scan_concurrency must be between 1 and 16")
	case s.InstanceScanTimeoutSeconds < 1:
		return invalidSettings("instance_scan_timeout_seconds must be at least 1")
	case s.PreviewTTLMinutes < 5 || s.PreviewTTLMinutes > 60:
		return invalidSettings("preview_ttl_minutes must be between 5 and 60")
	}

	return nil
}

// SettingsStore loads and saves Settings in Vault and remembers the last
// value it saw.
type SettingsStore struct {
	vault interfaces.Vault

	mu      sync.RWMutex
	current Settings
}

// NewSettingsStore returns a store whose current value is the defaults.
func NewSettingsStore(v interfaces.Vault) *SettingsStore {
	return &SettingsStore{vault: v, current: DefaultSettings()}
}

// Load reads the settings from Vault. A missing document yields the
// defaults, and a stored document is applied over the defaults so a field it
// leaves out keeps its default while a stored false or 0 stays as saved.
func (st *SettingsStore) Load(ctx context.Context) (Settings, error) {
	var stored map[string]interface{}

	found, err := st.vault.Get(ctx, SettingsVaultPath, &stored)
	if err != nil {
		return Settings{}, fmt.Errorf("load certificate settings: %w", err)
	}

	s := DefaultSettings()

	if found {
		raw, err := json.Marshal(stored)
		if err != nil {
			return Settings{}, fmt.Errorf("encode certificate settings: %w", err)
		}

		if err := json.Unmarshal(raw, &s); err != nil {
			return Settings{}, fmt.Errorf("decode certificate settings: %w", err)
		}
	}

	st.set(s)

	return s, nil
}

// Save validates s, writes it to Vault, and makes it the current value.
func (st *SettingsStore) Save(ctx context.Context, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}

	if err := st.vault.Put(ctx, SettingsVaultPath, s); err != nil {
		return fmt.Errorf("save certificate settings: %w", err)
	}

	st.set(s)

	return nil
}

// Current returns the last loaded or saved settings.
func (st *SettingsStore) Current() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()

	return st.current
}

func (st *SettingsStore) set(s Settings) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.current = s
}

package vault

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// unsealTimeout bounds one auto-unseal. The unseal runs apart from the request
// that triggered it, so the request's deadline cannot bound it.
const unsealTimeout = 60 * time.Second

// Static errors for err113 compliance.
var (
	ErrInvalidFilePath = errors.New("invalid file path")
	ErrUnsealPanicked  = errors.New("auto-unseal panicked")
)

// withAutoUnseal runs operation once and, when it fails because Vault is
// sealed or unreachable, tries an auto-unseal and runs it once more. The retry
// happens only when the error says Vault is sealed or the unseal unsealed it.
// Any other error is returned as it is after the one attempt, because a write
// whose answer was lost may already have landed, and a retry of the broker's
// own against a Vault that nothing changed would repeat it. This rule covers
// only the broker's attempts. The Vault API client underneath still retries a
// request on its own after a 5xx answer or a dropped connection, as its
// MaxRetries setting allows.
func (vault *Vault) withAutoUnseal(ctx context.Context, operation func() error) error {
	err := operation()
	if err == nil {
		return nil
	}

	if !vault.isSealedOrUnavailable(err) {
		return err
	}
	// A caller whose context has ended gets the original error. An unseal
	// and a retry under a dead context could only fail again.
	if ctx.Err() != nil {
		return err
	}

	if !vault.autoUnsealEnabled {
		return err
	}

	unsealed, uerr := vault.unsealBeforeRetry(ctx)
	if uerr != nil {
		return err // preserve the original error; the auto-unseal failed
	}

	if !unsealed && !isSealedError(err) {
		return err
	}

	return operation()
}

// unsealBeforeRetry runs the auto-unseal, or the test hook in its place, and
// reports whether it unsealed Vault. A hook that succeeds counts as an unseal.
func (vault *Vault) unsealBeforeRetry(ctx context.Context) (bool, error) {
	if vault.autoUnsealHook != nil {
		err := vault.autoUnsealHook(ctx)

		return err == nil, err
	}

	return vault.autoUnseal(ctx)
}

// isSealedError reports whether err says Vault is sealed, as against an error
// that only looks like an outage, such as a refused or reset connection.
func isSealedError(err error) bool {
	if errors.Is(err, vaultPkg.ErrIsSealed) {
		return true
	}

	return strings.Contains(strings.ToLower(err.Error()), "sealed")
}

// AutoUnsealIfSealed attempts to automatically unseal vault if it's sealed.
func (vault *Vault) AutoUnsealIfSealed(ctx context.Context) error {
	_, err := vault.autoUnseal(ctx)

	return err
}

// autoUnseal unseals Vault if it is sealed, and reports whether an unseal ran
// to success, whether this caller started it or waited on one in flight. A
// Vault that is not sealed, or a cooldown that holds the unseal off, is not an
// error and not an unseal.
func (vault *Vault) autoUnseal(ctx context.Context) (bool, error) {
	logger := logger.Get().Named("vault auto-unseal")

	err := vault.ensureClient()
	if err != nil {
		return false, err
	}

	health, err := vault.checkVaultHealth(ctx)
	if err != nil {
		return false, err
	}

	if !health.Sealed {
		return false, nil
	}

	err = vault.validateAutoUnsealConfig(logger)
	if err != nil {
		return false, err
	}

	call, leader := vault.joinOrStartUnseal()
	if call == nil {
		// The cooldown since the last attempt has not passed.
		return false, nil
	}

	if leader {
		vault.runUnsealDetached(ctx, call, logger)
	}

	err = awaitUnseal(ctx, call)
	if err != nil {
		return false, err
	}

	return true, nil
}

// unsealCall is one auto-unseal in flight. Every caller that finds Vault
// sealed while it runs waits for its result instead of starting another. err
// is written once, before done is closed, and read only after done is closed.
type unsealCall struct {
	done chan struct{}
	err  error
}

// joinOrStartUnseal returns the unseal in flight, or starts the record of a
// new one when the cooldown allows it. leader is true for the caller that must
// run the new unseal. A nil call means the cooldown since the last attempt has
// not passed and no unseal is in flight.
func (vault *Vault) joinOrStartUnseal() (*unsealCall, bool) {
	vault.mu.Lock()
	defer vault.mu.Unlock()

	if vault.unsealing != nil {
		return vault.unsealing, false
	}

	if vault.inUnsealCooldown() {
		return nil, false
	}

	vault.lastUnsealAttempt = time.Now()
	vault.unsealing = &unsealCall{done: make(chan struct{})}

	return vault.unsealing, true
}

// finishUnseal records the result of call, wakes every caller waiting on it,
// and clears it, so the next sealed Vault starts a new unseal once the
// cooldown has passed.
func (vault *Vault) finishUnseal(call *unsealCall, err error) {
	vault.mu.Lock()
	defer vault.mu.Unlock()

	call.err = err
	close(call.done)

	if vault.unsealing == call {
		vault.unsealing = nil
	}
}

// runUnsealDetached runs the unseal on a context that keeps ctx's values but
// not its cancellation, under its own bound. The unseal is a recovery action
// for every caller, and the cooldown has already been spent on it, so the
// caller that started it giving up must not cut it off and leave Vault sealed
// for the cooldown's length.
//
// A panic in the unseal becomes the call's error. The call is finished before
// the panic is logged, so the callers waiting on it are released and the next
// sealed Vault can start a new unseal, whatever the logging does.
func (vault *Vault) runUnsealDetached(ctx context.Context, call *unsealCall, logger logger.Logger) {
	unsealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unsealTimeout)

	go func() {
		defer cancel()

		var err error

		defer func() {
			recovered := recover()
			if recovered != nil {
				err = fmt.Errorf("%w: %v", ErrUnsealPanicked, recovered)
			}

			vault.finishUnseal(call, err)

			if recovered != nil {
				logger.Error("auto-unseal panicked: %v\n%s", recovered, debug.Stack())
			}
		}()

		err = vault.performUnseal(unsealCtx, logger)
	}()
}

// awaitUnseal waits for call's result or for ctx to end, whichever comes
// first. A caller that stops waiting leaves the unseal running for the others.
func awaitUnseal(ctx context.Context, call *unsealCall) error {
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return fmt.Errorf("stopped waiting for the vault unseal: %w", ctx.Err())
	}
}

func (vault *Vault) checkVaultHealth(ctx context.Context) (*api.HealthResponse, error) {
	health, err := vault.client.Sys().HealthWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check vault health during auto-unseal: %w", err)
	}

	return health, nil
}

func (vault *Vault) validateAutoUnsealConfig(logger logger.Logger) error {
	if vault.credentialsPath == "" {
		logger.Debug("vault is sealed but no credentials path configured; skipping auto-unseal")

		return vaultPkg.ErrSealedNoCredentials
	}

	return nil
}

// inUnsealCooldown reports whether the last unseal attempt is more recent than
// the cooldown. The caller holds vault.mu.
func (vault *Vault) inUnsealCooldown() bool {
	cooldown := vault.unsealCooldown
	if cooldown <= 0 {
		const defaultUnsealCooldownSeconds = 30

		cooldown = defaultUnsealCooldownSeconds * time.Second
	}

	return time.Since(vault.lastUnsealAttempt) < cooldown
}

func (vault *Vault) performUnseal(ctx context.Context, logger logger.Logger) error {
	key, err := vault.loadSealKey()
	if err != nil {
		logger.Error("failed to load seal key for auto-unseal: %s", err)

		return err
	}

	logger.Info("vault is sealed; attempting auto-unseal")

	err = vault.client.UnsealVault(ctx, key)
	if err != nil {
		logger.Error("auto-unseal failed: %s", err)

		return fmt.Errorf("failed to unseal vault: %w", err)
	}

	logger.Info("vault auto-unseal successful")

	return nil
}

// loadSealKey loads the vault seal key from the credentials file.
func (vault *Vault) loadSealKey() (string, error) {
	creds, err := vault.loadCredentialsFile()
	if err != nil {
		return "", err
	}

	if creds.SealKey == "" {
		return "", vaultPkg.ErrSealKeyNotFound
	}

	return creds.SealKey, nil
}

// isSealedOrUnavailable checks if an error indicates vault is sealed or unavailable.
func (vault *Vault) isSealedOrUnavailable(err error) bool {
	if err == nil {
		return false
	}

	var respErr *api.ResponseError
	if errors.As(err, &respErr) {
		// 503 is returned for sealed/standby/unavailable conditions
		if respErr.StatusCode == http.StatusServiceUnavailable {
			return true
		}
	}

	statusStr := strings.ToLower(err.Error())
	switch {
	case strings.Contains(statusStr, "sealed"):
		return true
	case strings.Contains(statusStr, "service unavailable"):
		return true
	case strings.Contains(statusStr, "connection refused"):
		return true
	case strings.Contains(statusStr, "connection reset"):
		return true
	case strings.Contains(statusStr, "broken pipe"):
		return true
	case strings.Contains(statusStr, "i/o timeout"):
		return true
	case strings.Contains(statusStr, "eof"):
		return true
	default:
		return false
	}
}

// IsSealedOrUnavailable is a public wrapper for testing purposes.
func (vault *Vault) IsSealedOrUnavailable(err error) bool {
	return vault.isSealedOrUnavailable(err)
}

// safeReadFile safely reads a file with path validation.
func safeReadFile(path string) ([]byte, error) {
	// Basic path validation - ensure it doesn't contain dangerous patterns
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("%w: %s", ErrInvalidFilePath, path)
	}

	data, err := os.ReadFile(path) // #nosec G304 - Path has been validated
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return data, nil
}

// SetAutoUnsealEnabled sets the auto-unseal enabled flag for testing purposes.
func (vault *Vault) SetAutoUnsealEnabled(enabled bool) {
	vault.autoUnsealEnabled = enabled
}

// SetAutoUnsealHook sets the auto-unseal hook for testing purposes.
func (vault *Vault) SetAutoUnsealHook(hook func(context.Context) error) {
	vault.autoUnsealHook = hook
}

// SetVerifyMountFn overrides the VerifyMount implementation for testing.
func (vault *Vault) SetVerifyMountFn(fn func(store string, createIfMissing bool) error) {
	vault.verifyMountFn = fn
}

// WithAutoUnseal is a public wrapper for testing purposes.
func (vault *Vault) WithAutoUnseal(ctx context.Context, operation func() error) error {
	return vault.withAutoUnseal(ctx, operation)
}

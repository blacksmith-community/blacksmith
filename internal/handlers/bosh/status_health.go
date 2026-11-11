package bosh

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	"blacksmith/internal/interfaces"
)

const (
	// defaultHealthTimeout bounds each reachability check.
	defaultHealthTimeout = 2 * time.Second

	// defaultHealthCacheTTL is how long a health result answers /b/status
	// before the next request checks again.
	defaultHealthCacheTTL = 5 * time.Second

	stateConnected   = "connected"
	stateUnreachable = "unreachable"
)

var errNotConfigured = errors.New("director is not configured")

// urlPattern matches the URLs inside an error message, which can carry
// credentials or tokens.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)

// healthResult is what one round of reachability checks found.
type healthResult struct {
	boshUp  bool
	vaultUp bool
}

func (r healthResult) healthy() bool { return r.boshUp && r.vaultUp }

func (r healthResult) boshState() string { return stateOf(r.boshUp) }

func (r healthResult) vaultState() string { return stateOf(r.vaultUp) }

func stateOf(up bool) string {
	if up {
		return stateConnected
	}

	return stateUnreachable
}

// healthCache holds the latest health result so that a UI polling
// /b/status does not call the director and Vault on every request.
type healthCache struct {
	timeout time.Duration
	ttl     time.Duration
	now     func() time.Time

	mu        sync.Mutex
	result    healthResult
	checkedAt time.Time
	valid     bool
}

func newHealthCache(timeout, ttl time.Duration, now func() time.Time) healthCache {
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}

	if ttl <= 0 {
		ttl = defaultHealthCacheTTL
	}

	if now == nil {
		now = time.Now
	}

	return healthCache{timeout: timeout, ttl: ttl, now: now}
}

// checkHealth returns the cached health when it is fresh, and otherwise
// checks the director and Vault together and caches what it finds. The
// lock is held through the check, so concurrent requests share one check.
func (h *Handler) checkHealth(ctx context.Context, logger interfaces.Logger) healthResult {
	cache := &h.health

	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.valid && cache.now().Sub(cache.checkedAt) < cache.ttl {
		return cache.result
	}

	result := h.probe(ctx, logger)

	// A caller that gave up would leave a false "unreachable" behind, so
	// only an answer to a live request is kept.
	if ctx.Err() == nil {
		cache.result = result
		cache.checkedAt = cache.now()
		cache.valid = true
	}

	return result
}

// probe asks the director and Vault whether they answer, each under its
// own timeout derived from the request context. The underlying errors go
// to the debug log only, since they can carry addresses and credentials.
func (h *Handler) probe(ctx context.Context, logger interfaces.Logger) healthResult {
	var (
		result healthResult
		group  sync.WaitGroup
	)

	group.Add(2)

	go func() {
		defer group.Done()

		result.boshUp = h.reachable(ctx, logger, "BOSH", h.directorHealth)
	}()

	go func() {
		defer group.Done()

		result.vaultUp = h.reachable(ctx, logger, "Vault", h.vaultHealth)
	}()

	group.Wait()

	return result
}

func (h *Handler) directorHealth(ctx context.Context) error {
	if h.director == nil {
		return errNotConfigured
	}

	_, err := h.director.GetInfo(ctx)
	if err != nil {
		return err //nolint:wrapcheck // logged at debug and never returned
	}

	return nil
}

// reachable runs one check under the health timeout. A check that is not
// configured counts as unreachable.
func (h *Handler) reachable(ctx context.Context, logger interfaces.Logger, name string, check func(context.Context) error) bool {
	if check == nil {
		logger.Debug("%s health check is not configured; reporting it unreachable", name)

		return false
	}

	checkCtx, cancel := context.WithTimeout(ctx, h.health.timeout)
	defer cancel()

	err := check(checkCtx)
	if err != nil {
		logger.Debug("%s health check failed: %s", name, urlPattern.ReplaceAllString(err.Error(), "<url>"))

		return false
	}

	return true
}

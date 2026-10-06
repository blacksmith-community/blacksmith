package boshapi

import "context"

// noRetriesKey marks a context whose requests are sent once.
type noRetriesKey struct{}

// WithoutRetries returns a context whose requests the client sends once,
// whatever their method, even when Config.RetryMax allows retries. Use it for
// a bounded call that must not stretch into minutes of backoff. A 401 still
// refreshes the token and resends the request once.
func WithoutRetries(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRetriesKey{}, true)
}

// RetriesDisabled reports whether ctx came from WithoutRetries.
func RetriesDisabled(ctx context.Context) bool {
	disabled, _ := ctx.Value(noRetriesKey{}).(bool)

	return disabled
}

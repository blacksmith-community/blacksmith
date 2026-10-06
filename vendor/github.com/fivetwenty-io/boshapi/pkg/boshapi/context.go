package boshapi

import "context"

// contextIDKey holds the context ID of a context's requests.
type contextIDKey struct{}

// WithContextID returns a context whose requests carry id in the
// X-Bosh-Context-Id header. The director records the ID on any task the
// request starts, so a request whose answer was lost can be looked up later
// with TaskListOptions.ContextID. The director accepts at most 64 characters,
// and the client refuses to send a longer ID. Use a fresh ID for each request
// that starts a task, because a lookup by ID finds every task that carries
// it. WithContextID panics when id is empty, since an empty ID would silently
// disable the correlation the caller asked for.
func WithContextID(ctx context.Context, id string) context.Context {
	if id == "" {
		panic("boshapi.WithContextID: the context ID is empty. Pass the ID to send in X-Bosh-Context-Id, or leave the context unchanged to send none")
	}

	return context.WithValue(ctx, contextIDKey{}, id)
}

// ContextIDFrom returns the context ID that WithContextID put in ctx, or an
// empty string when it holds none.
func ContextIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(contextIDKey{}).(string)

	return id
}

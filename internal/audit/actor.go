// Package audit carries who made a request, so records of certificate
// actions can say who acted and from where.
package audit

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"
)

// maxForwardedFor bounds the stored X-Forwarded-For value.
const maxForwardedFor = 256

// Actor is the authenticated caller of a request.
//
// RemoteAddr is the address the connection came from. ForwardedFor is what
// the client reported in X-Forwarded-For, and a client can put anything
// there. Both are for the record only, and nothing may base an
// authorisation decision on either.
type Actor struct {
	Username     string
	RemoteAddr   string
	ForwardedFor string
}

type actorKey struct{}

// WithActor returns a context that carries the actor.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor the context carries, if it has one.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)

	return a, ok
}

// ActorFromRequest builds the actor of an authenticated request. The
// forwarded-for value is cleaned of control characters, so it can't break
// a log line, and cut to 256 bytes.
func ActorFromRequest(req *http.Request, username string) Actor {
	return Actor{
		Username:     username,
		RemoteAddr:   req.RemoteAddr,
		ForwardedFor: truncate(clean(req.Header.Get("X-Forwarded-For")), maxForwardedFor),
	}
}

// From says where the request came from, as "<remote> via <forwarded-for>"
// when the client reported a forwarded address, and "<remote>" otherwise.
func (a Actor) From() string {
	if a.ForwardedFor == "" {
		return a.RemoteAddr
	}

	return a.RemoteAddr + " via " + a.ForwardedFor
}

// clean drops control characters, which includes newlines and tabs.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, s)
}

// truncate cuts s to at most limit bytes without splitting a character.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut]
}

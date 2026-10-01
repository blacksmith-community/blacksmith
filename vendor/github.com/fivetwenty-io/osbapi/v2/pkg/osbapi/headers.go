package osbapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// OSB API header constants.
const (
	// HeaderAPIVersion is the header for the OSB API version.
	HeaderAPIVersion = "X-Broker-API-Version"

	// HeaderOriginatingIdentity is the header for originating identity.
	HeaderOriginatingIdentity = "X-Broker-API-Originating-Identity"

	// HeaderRequestIdentity is the header for request identity (correlation).
	HeaderRequestIdentity = "X-Broker-API-Request-Identity"

	// APIVersion is the supported OSB API version.
	APIVersion = "2.17"
)

// OriginatingIdentity represents the identity of the requester.
type OriginatingIdentity struct {
	Platform string
	Value    map[string]any
}

// EncodeOriginatingIdentity encodes an OriginatingIdentity to the header format.
// Format: platform base64(json_value)
func EncodeOriginatingIdentity(identity OriginatingIdentity) (string, error) {
	valueBytes, err := json.Marshal(identity.Value)
	if err != nil {
		return "", fmt.Errorf("marshaling originating identity value: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(valueBytes)

	return identity.Platform + " " + encoded, nil
}

// DecodeOriginatingIdentity decodes the header value to an OriginatingIdentity.
func DecodeOriginatingIdentity(header string) (OriginatingIdentity, error) {
	var identity OriginatingIdentity

	// Find the space separator
	spaceIdx := -1
	for i, c := range header {
		if c == ' ' {
			spaceIdx = i
			break
		}
	}

	if spaceIdx < 0 {
		return identity, fmt.Errorf("invalid originating identity header: missing space separator")
	}

	identity.Platform = header[:spaceIdx]
	encodedValue := header[spaceIdx+1:]

	decoded, err := base64.StdEncoding.DecodeString(encodedValue)
	if err != nil {
		return identity, fmt.Errorf("decoding originating identity value: %w", err)
	}

	err = json.Unmarshal(decoded, &identity.Value)
	if err != nil {
		return identity, fmt.Errorf("unmarshaling originating identity value: %w", err)
	}

	return identity, nil
}

// contextKey is a private type for context keys defined in this package to
// avoid collisions with keys from other packages.
type contextKey struct{ name string }

// originatingIdentityKey is the context key under which a decoded
// OriginatingIdentity is stored by the OSB server middleware.
var originatingIdentityKey = contextKey{name: "originatingIdentity"}

// ContextWithOriginatingIdentity returns a copy of ctx with the given
// OriginatingIdentity attached. Brokers may use this to attach an identity
// in tests or in custom middleware.
func ContextWithOriginatingIdentity(ctx context.Context, id OriginatingIdentity) context.Context {
	return context.WithValue(ctx, originatingIdentityKey, id)
}

// OriginatingIdentityFromContext retrieves the OriginatingIdentity attached
// to ctx by the server middleware. The boolean is false when the request
// carried no X-Broker-API-Originating-Identity header (or when the header
// was malformed and the middleware chose to discard it).
func OriginatingIdentityFromContext(ctx context.Context) (OriginatingIdentity, bool) {
	id, ok := ctx.Value(originatingIdentityKey).(OriginatingIdentity)
	return id, ok
}

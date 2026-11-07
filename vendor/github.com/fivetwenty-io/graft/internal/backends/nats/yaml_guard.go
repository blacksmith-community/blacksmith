package natsbackend

import (
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
)

// decodeGuarded decodes the first YAML document in data, a value read
// from a NATS store, through the merge's own YAML layer. A value read
// from a NATS store is written by whoever can write the key, so the
// layer's guards matter here. Nesting past the depth limit fails before
// anything tokenizes it, aliases that would expand past the merge's node
// cap fail rather than expand, and every failure is
// yamlcodec.ErrExternalValue, whose text names no part of the value. The
// value reads the way a merge document reads, so plain yes and no are
// booleans and 010 is octal. JSON decodes the same way, since it is
// YAML.
func decodeGuarded(data []byte) (any, error) {
	return yamlcodec.DecodeExternal(data)
}

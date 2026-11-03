package natsbackend

import (
	"github.com/goccy/go-yaml"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// unmarshalGuarded decodes the first YAML document in data into out.
// It runs the data through yamlnode.FirstDocument before yaml.Unmarshal,
// which refuses nesting past the depth limit and cuts the data after the
// first document. A value read from a NATS store is written by whoever
// can write the key, and goccy's decoder spends close to a gigabyte on
// a few thousand unclosed brackets when it is given the data directly.
func unmarshalGuarded(data []byte, out any) error {
	first, err := yamlnode.FirstDocument(data)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(first, out)
}

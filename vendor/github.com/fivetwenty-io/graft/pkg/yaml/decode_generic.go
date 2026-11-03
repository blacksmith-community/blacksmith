package yaml

import (
	goyaml "github.com/goccy/go-yaml"

	"github.com/fivetwenty-io/graft/internal/yamlcodec"
)

// decodeGeneric decodes the document src returns into the values graft
// merge reads it as, which are map[string]any, []any, int, float64,
// bool, string, and nil, with YAML 1.1 booleans unless opts.YAML12 is set.
// An integer too large for int stays uint64. A nil value with a nil error
// means there is no document, or a null one. Every error it returns is a
// *DecodeError.
//
// For text it runs the same codec helpers as graft's engine, which are
// yamlcodec.FirstMergeDocument and the two halves of
// yamlcodec.DecodeGeneric, so a document decodes here exactly as a merge
// reads it, minus the engine's control-flow expansion of operators.
func decodeGeneric(src source, opts DecodeOptions) (any, error) {
	body, err := src(true)
	if err != nil {
		return nil, err
	}
	compat := &yamlcodec.YAMLCompat{ConvertYAML11Booleans: !opts.YAML12}
	var dec []goyaml.DecodeOption
	if opts.OnDuplicateKey != nil {
		dec = append(dec, goyaml.AllowDuplicateMapKey())
	}
	v, err := yamlcodec.DecodeGenericBody(body, compat, true, dec...)
	if err != nil {
		return nil, translate(err)
	}
	return v, nil
}

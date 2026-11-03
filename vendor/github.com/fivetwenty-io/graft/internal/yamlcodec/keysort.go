package yamlcodec

import sk "github.com/fivetwenty-io/graft/pkg/sortkeys"

// spruceKeyLess orders coerced map keys the way spruce's YAML encoder
// does; the comparison itself lives in sortkeys.Spruce so that the jq and
// yq commands sort the same way.
var spruceLess = sk.Less(sk.Spruce)

func spruceKeyLess(a, b string) bool {
	return spruceLess(a, b)
}

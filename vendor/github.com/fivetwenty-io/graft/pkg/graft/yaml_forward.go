package graft

import (
	"errors"

	goyaml "github.com/goccy/go-yaml"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlerr"
	_ "github.com/fivetwenty-io/graft/pkg/yaml" // sets yamlerr.Describe
)

// plainYAMLError returns err, a failure from the YAML codec. When err
// holds one of goccy's errors, whose text prints the source lines around
// the failure, the result's Error() is the text pkg/yaml's DecodeError
// gives instead: a line and a message that quotes nothing from the
// input. The detailed text is err's own. The codec's other errors quote
// nothing from the input, so they come back unchanged.
func plainYAMLError(err error) error {
	var ge goyaml.Error
	if !errors.As(err, &ge) {
		return err
	}
	return errtext.WithPlain(err, yamlerr.Text(err, "yaml: the document is not valid YAML"))
}

// YAMLHeadComment attaches a block of comment lines directly above one node
// in the output of MarshalYAMLWithComments. Each entry in Lines becomes one
// line prefixed with "# ". Path uses graft's and spruce's dotted
// document-path syntax, such as "meta.password", or "jobs.0.name" for a list
// index, with no leading "$.". MarshalYAMLWithComments converts the path
// internally to the bracket-indexed form that goccy/go-yaml requires
// ("$.jobs[0].name"), so callers never need to know that detail or depend on
// goccy/go-yaml's types directly.
type YAMLHeadComment = yamlcodec.YAMLHeadComment

// YAMLCompat controls YAML 1.1 backward compatibility behavior.
type YAMLCompat = yamlcodec.YAMLCompat

// MarshalYAML serializes a value to YAML with two spaces of indentation,
// which matches the output format that BOSH and CF ecosystem tools expect.
// It emits map keys in spruce's two-tier order. It writes every string, key
// or value, in the style that spruce's emitter picks for it (plain,
// single-quoted, double-quoted, or a literal block) and folds it at the same
// column. A key that spruce cannot write as a simple key uses the explicit
// "? key" form.
func MarshalYAML(v interface{}) ([]byte, error) {
	return yamlcodec.MarshalYAML(v)
}

// MarshalYAMLWith serializes v exactly like MarshalYAML, except that it
// orders the keys of every map with less instead of spruce's order. A nil
// less keeps spruce's order.
func MarshalYAMLWith(v interface{}, less func(a, b string) bool) ([]byte, error) {
	return yamlcodec.MarshalYAMLWith(v, less)
}

// MarshalYAMLWithComments serializes v exactly like MarshalYAML, with the
// same spruce key ordering and quoting rules, because both share one
// implementation and MarshalYAML is the call with no comments. It then
// attaches each entry in comments as a head comment directly above the node
// at its Path.
//
// A comments entry whose Path cannot be converted to a valid goccy/go-yaml
// path is skipped, and the encode does not fail. Comment placement is
// best-effort, and the document's own data never depends on it. The only
// current caller is "graft merge --report-deferred=inline" in cmd/graft,
// which attaches one comment per deferred key directly above that key.
func MarshalYAMLWithComments(v interface{}, comments []YAMLHeadComment) ([]byte, error) {
	return yamlcodec.MarshalYAMLWithComments(v, comments)
}

// MarshalYAMLWithCommentsWith serializes v exactly like
// MarshalYAMLWithComments, except that it orders the keys of every map with
// less. A nil less keeps spruce's order.
func MarshalYAMLWithCommentsWith(v interface{}, comments []YAMLHeadComment, less func(a, b string) bool) ([]byte, error) {
	return yamlcodec.MarshalYAMLWithCommentsWith(v, comments, less)
}

// QuoteInjectKeys pre-processes YAML bytes to quote graft's <<<: inject key.
// goccy/go-yaml rejects that key when it is unquoted, because it reads <<< as
// a variant of the YAML merge key <<. QuoteInjectKeys handles both the
// standalone form (<<<:) and the dotted-path form (foo.<<<:).
func QuoteInjectKeys(data []byte) []byte {
	return yamlcodec.QuoteInjectKeys(data)
}

// NormalizeMap converts every map[interface{}]interface{} value in the tree
// to a map[string]interface{}, however deeply it is nested. This is needed
// because other decoders, such as yaml.v2 through go-patch, produce a
// map[interface{}]interface{} when a map holds non-string keys, such as the
// integer keys 1: and 2:. goccy decodes those same maps into a
// map[string]interface{} with stringified keys.
func NormalizeMap(data map[string]interface{}) map[string]interface{} {
	return yamlcodec.NormalizeMap(data)
}

// DefaultYAMLCompat returns compat settings with YAML 1.1 booleans enabled.
func DefaultYAMLCompat() *YAMLCompat {
	return yamlcodec.DefaultYAMLCompat()
}

// ParseYAML11CompatAware parses data the same way that
// yaml.Unmarshal(data, &interface{}{}) would, except that it protects every
// explicitly quoted YAML 1.1 boolean lookalike ("yes", 'On', "OFF", and so
// on) from YAMLCompat's later coercion of yes, no, on, and off to booleans.
// Unquoted occurrences of those words decode as plain strings exactly as
// before, so ConvertMapValues keeps converting them.
//
// Quoting information exists only at the token level. Once a document is
// decoded into interface{} values, a quoted "yes" and an unquoted one are
// indistinguishable Go strings. ParseYAML11CompatAware therefore parses data
// into goccy's AST and tags the quoted matches using the AST's own token
// type, rather than guessing from the text. It then decodes the locally
// mutated AST back into the same generic shape that a direct yaml.Unmarshal
// call would produce.
//
// Callers must always run the result through UnprotectYAML11QuotedBools after
// applying YAMLCompat. Otherwise a tagged value that YAMLCompat left alone
// (when compat is disabled, for example) would come out as the internal
// marker-prefixed string instead of the original word.
//
// Library callers pass raw input through FirstMergeDocument first, because
// goccy's parser stalls on nesting that FirstMergeDocument fails cheaply.
func ParseYAML11CompatAware(data []byte) (interface{}, error) {
	return yamlcodec.ParseYAML11CompatAware(data)
}

// FirstMergeDocument returns the first document of data, which is raw YAML
// about to be merged. The merge reads no other document, and neither does
// spruce. goccy parses every document it is given, so a syntax error or deep
// nesting in a later document would otherwise fail the merge. Like spruce,
// FirstMergeDocument first strips a leading UTF-8 byte order mark, because
// goccy reads one as text. The result is a prefix of the rest of data, with
// every CRLF and lone CR turned into an LF. It is all of that rest when
// nothing ends the first document.
//
// It fails a first document that nests deeper than 10,000 levels, and it
// runs before anything else tokenizes data. That order matters because
// goccy's parser slows down faster than linearly with depth, and 20,000
// unclosed brackets cost it hundreds of megabytes before it fails. The error
// reads as the text that a merge gives for a tree nested past 4,096 levels,
// so every over-deep merge fails with one message whatever its depth.
//
// Content after the "..." that ends the first document needs a "---" before
// it, and spruce fails a file without one. The merge must not drop that
// content, so FirstMergeDocument fails it with a parse error that carries
// yaml.v3's message and line.
func FirstMergeDocument(data []byte) ([]byte, error) {
	first, err := yamlcodec.FirstMergeDocument(data)
	if err != nil {
		var tooDeep *yamlcodec.MaxRecursionError
		if errors.As(err, &tooDeep) {
			return nil, err
		}
		cause := plainYAMLError(err)
		return nil, NewParseError(cause.Error(), cause)
	}
	return first, nil
}

// CheckSelfContainingAnchors fails data, which is YAML bytes about to be
// decoded, when an alias in its first document sits inside the collection
// that its anchor names. The error carries the message that spruce gives.
// goccy decodes such an alias as null, so without the check the value would
// quietly vanish. A syntax error passes the check, so the caller's own decode
// reports it. Library callers pass raw input through FirstMergeDocument
// first, because goccy's parser stalls on nesting that FirstMergeDocument
// fails cheaply.
func CheckSelfContainingAnchors(data []byte) error {
	return yamlcodec.CheckSelfContainingAnchors(data)
}

// UnprotectYAML11QuotedBools reverses the tagging that ParseYAML11CompatAware
// applied. It walks v and strips the internal marker from any string value
// that still carries it, which restores the original bare word. It mutates
// map and slice values in place and returns the root value, which may be a
// replacement, in the same in-place style as ConvertMapValues.
func UnprotectYAML11QuotedBools(v interface{}) interface{} {
	return yamlcodec.UnprotectYAML11QuotedBools(v)
}

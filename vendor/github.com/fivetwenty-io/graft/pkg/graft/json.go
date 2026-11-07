package graft

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
	"github.com/fivetwenty-io/graft/internal/yamlprep"
)

func jsonifyData(data []byte, strict bool) (string, error) {
	return jsonifyDataWith(data, strict, nil)
}

// jsonifyDataWith is jsonifyData with the object key order chosen by
// less. A nil less leaves json.Marshal's byte order.
func jsonifyDataWith(data []byte, strict bool, less func(a, b string) bool) (string, error) {
	// spruce's json reads only the first document of each part, and a
	// part can hold more than one, since JSONifyFiles splits only at a
	// bare "---" line. So the part is cut to its first document the way
	// the merge cuts its input, before anything tokenizes the rest, and
	// nesting past 10,000 levels fails first. spruce's json reports a
	// depth error, not the recursion text its merge gives, so this keeps
	// yaml.v3's depth error.
	data, err := yamlnode.FirstDocument(yamlnode.TrimBOM(data))
	if err != nil {
		return "", errtext.Colorf("@R{Root of YAML document is not a hash/map}: %s\n", plainYAMLError(err))
	}

	// Parse through the same YAML-1.1-compat-aware path ParseYAML uses, so
	// `graft json` and `graft merge` agree on unquoted yes/no/on/off ->
	// bool coercion and on quoted lookalikes staying strings, matching
	// spruce json on both counts. The input goes through the same
	// yamlprep rewrites ParseYAML applies, so an unquoted {{...}}
	// placeholder reads as a string here as it does in merge and diff.
	data, _ = yamlprep.Prepare(data)
	root, err := ParseYAML11CompatAware(data)
	var tooDeep *yamlcodec.MaxRecursionError
	if errors.As(err, &tooDeep) {
		// The document is a map whose aliases expand too far, so the
		// root-type prefix would misdescribe it.
		return "", err
	}
	if err != nil {
		return "", errtext.Colorf("@R{Root of YAML document is not a hash/map}: %s\n", plainYAMLError(err))
	}

	// An empty, whitespace-only, or explicit-null document unmarshals to a
	// nil root rather than an error. Require an actual map root here (as
	// spruce's simpleyaml.Map() does) so a dangling multi-doc separator or
	// blank document fails loudly instead of silently emitting "{}".
	doc, ok := root.(map[string]interface{})
	if !ok {
		return "", errtext.Colorf("@R{Root of YAML document is not a hash/map}: %s\n", "type assertion to map[string]interface{} failed")
	}

	doc = DefaultYAMLCompat().ConvertAndUnprotect(doc)

	if strict {
		// Every key is a string by now, so --strict reads the keys off
		// the parsed document instead.
		nonString, err := hasNonStringKey(data)
		if err != nil {
			return "", err
		}
		if nonString {
			return "", errors.New(strictKeyMessage)
		}
	}

	doc_, err := deinterface(doc, strict)
	if err != nil {
		return "", err
	}

	var b []byte
	if less == nil {
		b, err = json.Marshal(doc_)
	} else {
		b, err = marshalOrdered(doc_, less)
	}
	if err != nil {
		return "", err
	}

	return string(b), nil
}

// marshalOrdered writes v as compact JSON the way json.Marshal does, but
// orders every object's keys with less instead of byte order. Scalars go
// through json.Marshal, so their text matches the default path.
func marshalOrdered(v interface{}, less func(a, b string) bool) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeOrdered(&buf, v, less); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeOrdered(buf *bytes.Buffer, v interface{}, less func(a, b string) bool) error {
	switch t := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.SliceStable(keys, func(i, j int) bool { return less(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeOrdered(buf, t[k], less); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []interface{}:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeOrdered(buf, item, less); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}

// strictKeyMessage is what `json --strict` fails with, word for word what
// spruce prints, when a mapping has a key that is not a string.
const strictKeyMessage = "non-string keys found during strict JSON conversion"

// hasNonStringKey reports whether any mapping in the first document of
// data has a key that spruce reads as something other than a string,
// which is the test behind `json --strict`. The aliases are expanded
// first, so a key given by an alias and a key merged in with "<<" are
// read as spruce reads them.
func hasNonStringKey(data []byte) (bool, error) {
	file, err := yamlnode.ParseBytes(data, 0)
	if err != nil {
		return false, err
	}
	body := yamlnode.FirstBody(file)
	if body == nil {
		return false, nil
	}
	// ExpandAliases and not ExpandBody, since the fold of the merge keys
	// can only drop a key, never add one. A key that a << brings in is
	// still in the tree under the <<, where the walk finds it.
	if body, err = yamlcodec.ExpandAliases(body); err != nil {
		return false, err
	}
	finder := &nonStringKeyFinder{}
	ast.Walk(finder, body)
	return finder.found, nil
}

// nonStringKeyFinder is an ast.Visitor that records whether any mapping
// entry it visits has a key that is not a string.
type nonStringKeyFinder struct{ found bool }

func (f *nonStringKeyFinder) Visit(n ast.Node) ast.Visitor {
	if f.found || n == nil {
		return nil
	}
	if entry, ok := n.(*ast.MappingValueNode); ok && isNonStringKey(entry.Key) {
		f.found = true
		return nil
	}
	return f
}

// isNonStringKey reports whether key reads as an integer, a float, a
// boolean, or null. A plain yes, no, on, or off counts as a boolean, as
// the value conversion treats it. A quoted scalar, a key tagged !!str,
// and the "<<" merge key do not count.
func isNonStringKey(key ast.Node) bool {
	switch k := key.(type) {
	case *ast.MappingKeyNode:
		return isNonStringKey(k.Value)
	case *ast.AnchorNode:
		return isNonStringKey(k.Value)
	case *ast.TagNode:
		if k.Start == nil {
			return false
		}
		switch k.Start.Value {
		case "!!int", "!!float", "!!bool", "!!null":
			return true
		}
		return false
	case *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.NullNode, *ast.InfinityNode, *ast.NanNode:
		return true
	case *ast.StringNode:
		if k.Token == nil || yamlcodec.IsQuotedScalarToken(k.Token.Type) {
			return false
		}
		if yamlcodec.Yaml11BoolLookalikeWords[k.Value] {
			return true
		}
		switch yamlnode.ResolvePlainTag(k.Value) {
		case yamlnode.TagInt, yamlnode.TagFloat, yamlnode.TagBool, yamlnode.TagNull:
			return true
		}
		return false
	default:
		return false
	}
}

// splitYAMLDocs splits raw multi-document YAML on the "\n---\n" document
// separator, the same convention spruce's JSONifyFiles uses. A leading
// empty document (e.g. input starting with "---\n") is dropped so index
// numbers in error messages line up with the document's actual position.
func splitYAMLDocs(data []byte) [][]byte {
	docs := bytes.Split(data, []byte("\n---\n"))
	if len(docs[0]) == 0 {
		docs = docs[1:]
	}
	return docs
}

// JSONifyIO reads from a reader and converts to JSON format.
func JSONifyIO(in io.Reader, strict bool) (string, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return "", errtext.Colorf("@R{Error reading input}: %s", err)
	}
	return jsonifyData(data, strict)
}

// JSONifyFiles reads files and converts them to JSON format.
func JSONifyFiles(paths []string, strict bool) ([]string, error) {
	return JSONifyFilesWith(paths, strict, nil)
}

// JSONifyFilesWith reads files and converts them to JSON format, ordering
// object keys with less. A nil less leaves the encoder's byte order.
func JSONifyFilesWith(paths []string, strict bool, less func(a, b string) bool) ([]string, error) {
	l := []string{}
	var err error
	for _, path := range paths {
		data := []byte{}
		if path == "-" {
			DEBUG("Processing STDIN")
			stat, statErr := os.Stdin.Stat()
			if statErr != nil {
				return nil, errtext.Colorf("@R{Error statting STDIN} - Bailing out: %s\n", statErr)
			}
			if stat.Mode()&os.ModeCharDevice == 0 {
				data, err = io.ReadAll(os.Stdin)
				if err != nil {
					return nil, errtext.Colorf("@R{Error reading STDIN}: %s\n", err)
				}
			}
		} else {
			DEBUG("Processing file '%s'", path)
			// #nosec G304 - File path is from user-provided command line arguments which is expected behavior for processing YAML files
			data, err = os.ReadFile(path)
			if err != nil {
				return nil, errtext.Colorf("@R{Error reading file} @m{%s}: %s", path, err)
			}
		}

		docs := splitYAMLDocs(data)
		for i, doc := range docs {
			jsonData, err := jsonifyDataWith(doc, strict, less)
			if err != nil {
				return nil, errtext.Colorf("%s[%d]: %s", path, i, err)
			}
			l = append(l, jsonData)
		}
	}

	return l, nil
}

// readJSONSource reads the raw bytes for one JSONifyFiles/YAMLifyFiles
// input path, applying the same stdin ("-") convention both directions
// share: reading os.Stdin only if it is actually piped (not a character
// device), and erroring by path name on any read failure.
func readJSONSource(path string) ([]byte, error) {
	if path == "-" {
		DEBUG("Processing STDIN")
		stat, statErr := os.Stdin.Stat()
		if statErr != nil {
			return nil, errtext.Colorf("@R{Error statting STDIN} - Bailing out: %s\n", statErr)
		}
		if stat.Mode()&os.ModeCharDevice != 0 {
			return []byte{}, nil
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, errtext.Colorf("@R{Error reading STDIN}: %s\n", err)
		}
		return data, nil
	}

	DEBUG("Processing file '%s'", path)
	// #nosec G304 - File path is from user-provided command line arguments which is expected behavior for processing YAML/JSON files
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errtext.Colorf("@R{Error reading file} @m{%s}: %s", path, err)
	}
	return data, nil
}

// YAMLifyFiles reads files (or stdin via "-") containing JSON and converts
// each top-level JSON value found to a YAML document. Input may be either
// one compact JSON document per line (the shape JSONifyFiles/`graft json`
// itself produces, enabling a `graft json | graft json --reverse`
// round-trip) or a single pretty-printed, multi-line JSON document (e.g.
// piped from `curl`); a streaming decoder is used so both shapes work
// without a format flag. Each returned string is one YAML document with no
// trailing newline and no `---` separator; the caller decides how to join
// multiple documents.
func YAMLifyFiles(paths []string) ([]string, error) {
	out := []string{}
	for _, path := range paths {
		data, err := readJSONSource(path)
		if err != nil {
			return nil, err
		}

		docs, decErr := decodeJSONDocs(data)
		if decErr != nil {
			return nil, errtext.Colorf("@m{%s}: @R{Error parsing JSON}: %s\n", path, decErr)
		}
		if len(docs) == 0 {
			return nil, errtext.Colorf("@m{%s}: @R{Error parsing JSON}: no JSON documents found\n", path)
		}

		for i, doc := range docs {
			yamlBytes, mErr := MarshalYAML(doc)
			if mErr != nil {
				return nil, errtext.Colorf("@m{%s}[%d]: @R{Error converting JSON to YAML}: %s\n", path, i, mErr)
			}
			out = append(out, strings.TrimRight(string(yamlBytes), "\n"))
		}
	}
	return out, nil
}

// decodeJSONDocs decodes every top-level JSON value present in data using a
// streaming decoder, so it accepts both newline-delimited compact JSON
// ("JSON Lines") and a single pretty-printed multi-line JSON value
// identically. Empty (whitespace-only) input yields zero documents and no
// error; the caller decides whether that is itself an error.
func decodeJSONDocs(data []byte) ([]interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	// UseNumber preserves the source text of every JSON number as a
	// json.Number instead of collapsing every number to float64 (Go's
	// encoding/json default for interface{} targets). Without this,
	// `graft json --reverse` on `{"port":5432}` would emit `port: 5432.0`
	// instead of `port: 5432`. normalizeJSONNumbers converts each
	// json.Number back to int64 (whole numbers) or float64 (fractional)
	// before YAML marshaling.
	dec.UseNumber()
	docs := []interface{}{}
	for {
		var v interface{}
		if err := dec.Decode(&v); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		docs = append(docs, normalizeJSONNumbers(v))
	}
	return docs, nil
}

// normalizeJSONNumbers recursively converts every json.Number produced by a
// UseNumber()-enabled decode into an int64 (when the source text has no
// fractional/exponent part and fits in 64 bits) or a float64 otherwise, so
// YAML marshaling renders whole numbers without a trailing ".0".
func normalizeJSONNumbers(v interface{}) interface{} {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		// Unreachable for well-formed JSON numbers, but fall back to the
		// original string representation rather than dropping data.
		return t.String()
	case map[string]interface{}:
		for k, sub := range t {
			t[k] = normalizeJSONNumbers(sub)
		}
		return t
	case []interface{}:
		for i, sub := range t {
			t[i] = normalizeJSONNumbers(sub)
		}
		return t
	default:
		return v
	}
}

// CombineJSONLines wraps a set of already-serialized compact JSON documents
// (as produced by jsonifyData, one per YAML document) into a single
// pretty-printed JSON array. This backs `graft json --multi-doc`, which
// trades the default one-JSON-object-per-line output for a single
// machine-parseable array value. A nil/empty input produces "[]" rather
// than an error, matching encoding/json's own empty-slice marshaling.
func CombineJSONLines(lines []string) (string, error) {
	raws := make([]json.RawMessage, len(lines))
	for i, line := range lines {
		raws[i] = json.RawMessage(line)
	}

	b, err := json.MarshalIndent(raws, "", "  ")
	if err != nil {
		return "", errtext.Colorf("@R{Error combining JSON documents into an array}: %s", err)
	}
	return string(b), nil
}

func deinterface(o interface{}, strict bool) (interface{}, error) {
	switch v := o.(type) {
	case map[string]interface{}:
		return deinterfaceMap(v, strict)
	case []interface{}:
		return deinterfaceList(v, strict)
	default:
		return o, nil
	}
}

func addKeyToMap(m map[string]interface{}, k, v interface{}, strict bool) error {
	vs := fmt.Sprintf("%v", k)
	_, exists := m[vs]
	if exists {
		NewWarningError(eContextAll, "@Y{Duplicate key detected: %s}", vs).Warn()
		return nil
	}
	dv, err := deinterface(v, strict)
	if err != nil {
		return err
	}
	m[vs] = dv
	return nil
}

func deinterfaceMap(o map[string]interface{}, strict bool) (map[string]interface{}, error) {
	m := map[string]interface{}{}
	for k, v := range o {
		err := addKeyToMap(m, k, v, strict)
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func deinterfaceList(o []interface{}, strict bool) ([]interface{}, error) {
	l := make([]interface{}, len(o))
	for i, v := range o {
		v_, err := deinterface(v, strict)
		if err != nil {
			return nil, err
		}
		l[i] = v_
	}
	return l, nil
}

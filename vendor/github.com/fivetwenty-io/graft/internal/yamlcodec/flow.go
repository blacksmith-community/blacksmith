package yamlcodec

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// flowPlainSafe matches the strings written plain inside a flow
// collection. It leaves out every character a flow indicator, a comment,
// or a key separator could use.
var flowPlainSafe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

// flowText writes v, a yaml.MapSlice, a []interface{}, or a scalar, as a
// one-line flow collection. goccy lays out the collection, and every
// string that is not plainly safe, and every PlainScalar, stands in for
// its text as a placeholder that is replaced once goccy has written the
// line. It reports false when a string holds the placeholder prefix.
func (e *scalarEncoder) flowText(v interface{}) (string, bool) {
	prefix := e.prefix + "flow"
	var texts []string
	prepared, ok := e.flowPrepare(v, prefix, &texts)
	if !ok {
		return "", false
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf, yaml.Flow(true), yaml.Indent(2))
	if err := enc.Encode(prepared); err != nil {
		return "", false
	}
	if err := enc.Close(); err != nil {
		return "", false
	}
	line := strings.TrimSuffix(buf.String(), "\n")
	re := regexp.MustCompile(regexp.QuoteMeta(prefix) + `([0-9]+)_`)
	return re.ReplaceAllStringFunc(line, func(m string) string {
		idx, err := strconv.Atoi(m[len(prefix) : len(m)-1])
		if err != nil || idx >= len(texts) {
			return m
		}
		return texts[idx]
	}), true
}

// flowPrepare rebuilds v for goccy's flow encoder.
func (e *scalarEncoder) flowPrepare(v interface{}, prefix string, texts *[]string) (interface{}, bool) {
	stand := func(text string) string {
		*texts = append(*texts, text)
		return prefix + strconv.Itoa(len(*texts)-1) + "_"
	}
	switch val := v.(type) {
	case string:
		if strings.Contains(val, e.prefix) {
			return nil, false
		}
		if e.flowPlain(val) {
			return val, true
		}
		return stand(strconv.Quote(val)), true
	case PlainScalar:
		if strings.Contains(string(val), e.prefix) {
			return nil, false
		}
		return stand(string(val)), true
	case yaml.MapSlice:
		return e.flowMap(val, prefix, texts)
	case []interface{}:
		return e.flowList(val, prefix, texts)
	case nil, bool, int, int64, uint64:
		return v, true
	default:
		return stand(fmt.Sprintf("%v", v)), true
	}
}

// flowPlain reports whether s can be written plain in a flow collection.
func (e *scalarEncoder) flowPlain(s string) bool {
	if !flowPlainSafe.MatchString(s) || !yamlV2ResolvesToString(s) || isBase60Float(s) {
		return false
	}
	return !e.quoteTimestamps || !readsAsTimestamp(s)
}

// flowMap is flowPrepare for a yaml.MapSlice.
func (e *scalarEncoder) flowMap(val yaml.MapSlice, prefix string, texts *[]string) (interface{}, bool) {
	out := make(yaml.MapSlice, 0, len(val))
	for _, item := range val {
		key, ok := e.flowPrepare(item.Key, prefix, texts)
		if !ok {
			return nil, false
		}
		value, ok := e.flowPrepare(item.Value, prefix, texts)
		if !ok {
			return nil, false
		}
		out = append(out, yaml.MapItem{Key: key, Value: value})
	}
	return out, true
}

// flowList is flowPrepare for a []interface{}.
func (e *scalarEncoder) flowList(val []interface{}, prefix string, texts *[]string) (interface{}, bool) {
	out := make([]interface{}, len(val))
	for i, item := range val {
		prepared, ok := e.flowPrepare(item, prefix, texts)
		if !ok {
			return nil, false
		}
		out[i] = prepared
	}
	return out, true
}

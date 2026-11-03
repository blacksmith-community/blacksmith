// Copyright (c) 2011-2019 Canonical Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Ported from go.yaml.in/yaml/v3 v3.0.4 encode.go, yaml.go, and resolve.go
// and modified for graft: the encoder builds Nodes directly instead of
// emitting text, choosing each tag the way a reparse of the emitted text
// would, and it returns errors where yaml.v3 panics.

package yamlnode

import (
	"encoding"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// UnsupportedTypeError is what yaml.v3's encoder panics with for chan,
// func, complex, and unsafe pointer values.
type UnsupportedTypeError struct{ Type reflect.Type }

// Error reports the type the encoder cannot marshal.
func (e *UnsupportedTypeError) Error() string {
	return "cannot marshal type: " + e.Type.String()
}

// marshaler is yaml.v3's Marshaler interface.
type marshaler interface {
	MarshalYAML() (interface{}, error)
}

// FromValue returns the DocumentNode that yaml.v3's Unmarshal(Marshal(v))
// would produce, without producing text. The tree never carries anchors,
// aliases, comments, or positions.
func FromValue(v interface{}) (*Node, error) {
	root, err := encodeValue(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	return &Node{Kind: DocumentNode, Content: []*Node{root}}, nil
}

// encodeValue follows encoder.marshal: nil and the special types first,
// then the value's kind.
func encodeValue(in reflect.Value) (*Node, error) {
	if !in.IsValid() || in.Kind() == reflect.Pointer && in.IsNil() {
		return nullNode(), nil
	}
	switch value := in.Interface().(type) {
	case time.Time:
		return plainNode(value.Format(time.RFC3339Nano)), nil
	case *time.Time:
		return plainNode(value.Format(time.RFC3339Nano)), nil
	case time.Duration:
		return stringNode(value.String()), nil
	case marshaler:
		v, err := value.MarshalYAML()
		if err != nil {
			return nil, err
		}
		if v == nil {
			return nullNode(), nil
		}
		return encodeValue(reflect.ValueOf(v))
	case encoding.TextMarshaler:
		text, err := value.MarshalText()
		if err != nil {
			return nil, err
		}
		return stringNode(string(text)), nil
	case nil:
		return nullNode(), nil
	}
	return encodeKind(in)
}

// encodeKind dispatches on the kind of a value that has no special
// marshaling.
func encodeKind(in reflect.Value) (*Node, error) {
	switch in.Kind() {
	case reflect.Interface, reflect.Pointer:
		return encodeValue(in.Elem())
	case reflect.Map:
		return encodeMap(in)
	case reflect.Struct:
		return encodeStruct(in)
	case reflect.Slice, reflect.Array:
		return encodeSlice(in)
	case reflect.String:
		return stringNode(in.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return plainNode(strconv.FormatInt(in.Int(), 10)), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return plainNode(strconv.FormatUint(in.Uint(), 10)), nil
	case reflect.Float32, reflect.Float64:
		return floatNode(in), nil
	case reflect.Bool:
		return plainNode(strconv.FormatBool(in.Bool())), nil
	default:
		return nil, &UnsupportedTypeError{Type: in.Type()}
	}
}

func nullNode() *Node {
	return &Node{Kind: ScalarNode, Tag: TagNull, Value: "null"}
}

// plainNode builds a scalar the emitter writes plain, so a reparse
// resolves its tag from the text.
func plainNode(text string) *Node {
	return &Node{Kind: ScalarNode, Tag: ResolvePlainTag(text), Value: text}
}

func floatNode(in reflect.Value) *Node {
	bits := 64
	if in.Kind() == reflect.Float32 {
		bits = 32
	}
	s := strconv.FormatFloat(in.Float(), 'g', -1, bits)
	switch s {
	case "+Inf":
		s = ".inf"
	case "-Inf":
		s = "-.inf"
	case "NaN":
		s = ".nan"
	}
	return plainNode(s)
}

// stringNode follows encoder.stringv. A string that would resolve to
// another tag when plain is quoted and stays a string, except "<<", which
// yaml.v3 reads back as a merge key even when it is quoted. Invalid UTF-8
// becomes base64 under !!binary.
func stringNode(s string) *Node {
	if !utf8.ValidString(s) {
		return &Node{Kind: ScalarNode, Tag: TagBinary, Value: encodeBase64(s)}
	}
	if s == "<<" {
		return &Node{Kind: ScalarNode, Tag: TagMerge, Value: s}
	}
	return &Node{Kind: ScalarNode, Tag: TagStr, Value: s}
}

func encodeMap(in reflect.Value) (*Node, error) {
	keys := in.MapKeys()
	SortKeys(keys)
	m := &Node{Kind: MappingNode, Tag: TagMap}
	for _, k := range keys {
		if err := appendPair(m, k, in.MapIndex(k)); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func encodeSlice(in reflect.Value) (*Node, error) {
	s := &Node{Kind: SequenceNode, Tag: TagSeq}
	for i := 0; i < in.Len(); i++ {
		item, err := encodeValue(in.Index(i))
		if err != nil {
			return nil, err
		}
		s.Content = append(s.Content, item)
	}
	return s, nil
}

// appendPair encodes a key and its value and adds both to a mapping.
func appendPair(m *Node, key, value reflect.Value) error {
	k, err := encodeValue(key)
	if err != nil {
		return err
	}
	v, err := encodeValue(value)
	if err != nil {
		return err
	}
	m.Content = append(m.Content, k, v)
	return nil
}

func encodeStruct(in reflect.Value) (*Node, error) {
	sinfo, err := getStructInfo(in.Type())
	if err != nil {
		return nil, err
	}
	m := &Node{Kind: MappingNode, Tag: TagMap}
	for _, info := range sinfo.fields {
		value := in.Field(info.num)
		if info.inline != nil {
			value = fieldByIndex(in, info.inline)
			if !value.IsValid() {
				continue
			}
		}
		if info.omitEmpty && isZero(value) {
			continue
		}
		if err := appendPair(m, reflect.ValueOf(info.key), value); err != nil {
			return nil, err
		}
	}
	if sinfo.inlineMap >= 0 {
		if err := appendInlineMap(m, sinfo, in.Field(sinfo.inlineMap)); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// appendInlineMap adds the entries of an inlined map after the struct's
// own fields, and rejects a key that a field already uses.
func appendInlineMap(m *Node, sinfo *structInfo, inline reflect.Value) error {
	if inline.Len() == 0 {
		return nil
	}
	keys := inline.MapKeys()
	SortKeys(keys)
	for _, k := range keys {
		if sinfo.names[k.String()] {
			return fmt.Errorf("cannot have key %q in inlined map: conflicts with struct field", k.String())
		}
		if err := appendPair(m, k, inline.MapIndex(k)); err != nil {
			return err
		}
	}
	return nil
}

// fieldByIndex walks an inline index path, following pointers, and
// returns an invalid Value when a pointer on the way is nil.
func fieldByIndex(v reflect.Value, index []int) reflect.Value {
	for _, num := range index {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}
		v = v.Field(num)
	}
	return v
}

// structField is what the encoder keeps of yaml.v3's fieldInfo. The flow
// flag only changes the emitted style, so it is validated and dropped.
type structField struct {
	key       string
	num       int
	omitEmpty bool
	inline    []int // path to a field of an inlined struct; nil for a direct field
}

type structInfo struct {
	fields    []structField
	names     map[string]bool
	inlineMap int // index of the ",inline" map field, or -1
}

// add appends a field and rejects a key that another field already uses.
func (s *structInfo) add(f structField, st reflect.Type) error {
	if s.names[f.key] {
		return errors.New("duplicated key '" + f.key + "' in struct " + st.String())
	}
	s.names[f.key] = true
	s.fields = append(s.fields, f)
	return nil
}

// getStructInfo ports yaml.v3's getStructInfo without its cache. It does
// not skip an inlined struct whose pointer implements yaml.v3's
// Unmarshaler, because that interface takes a yaml.Node.
func getStructInfo(st reflect.Type) (*structInfo, error) {
	sinfo := &structInfo{names: map[string]bool{}, inlineMap: -1}
	for i := 0; i < st.NumField(); i++ {
		field := st.Field(i)
		if field.PkgPath != "" && !field.Anonymous {
			continue // Private field.
		}
		tag := field.Tag.Get("yaml")
		if tag == "" && !strings.Contains(string(field.Tag), ":") {
			tag = string(field.Tag)
		}
		if tag == "-" {
			continue
		}
		name, omitEmpty, inline, err := parseFieldTag(tag, st)
		if err != nil {
			return nil, err
		}
		if inline {
			if err := sinfo.addInline(st, i, field); err != nil {
				return nil, err
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		if err := sinfo.add(structField{key: name, num: i, omitEmpty: omitEmpty}, st); err != nil {
			return nil, err
		}
	}
	return sinfo, nil
}

// parseFieldTag splits a yaml struct tag into its key and flags.
func parseFieldTag(tag string, st reflect.Type) (name string, omitEmpty, inline bool, err error) {
	parts := strings.Split(tag, ",")
	if len(parts) == 1 {
		return tag, false, false, nil
	}
	for _, flag := range parts[1:] {
		switch flag {
		case "omitempty":
			omitEmpty = true
		case "flow":
		case "inline":
			inline = true
		default:
			return "", false, false, fmt.Errorf("unsupported flag %q in tag %q of type %s", flag, tag, st)
		}
	}
	return parts[0], omitEmpty, inline, nil
}

// addInline handles a ",inline" field: a map with string keys, or a
// struct (or pointer to one) whose fields join the parent's.
func (s *structInfo) addInline(st reflect.Type, i int, field reflect.StructField) error {
	switch field.Type.Kind() {
	case reflect.Map:
		if s.inlineMap >= 0 {
			return errors.New("multiple ,inline maps in struct " + st.String())
		}
		if field.Type.Key() != reflect.TypeOf("") {
			return errors.New("option ,inline needs a map with string keys in struct " + st.String())
		}
		s.inlineMap = i
		return nil
	case reflect.Struct, reflect.Pointer:
		ftype := field.Type
		for ftype.Kind() == reflect.Pointer {
			ftype = ftype.Elem()
		}
		if ftype.Kind() != reflect.Struct {
			return errors.New("option ,inline may only be used on a struct or map field")
		}
		sub, err := getStructInfo(ftype)
		if err != nil {
			return err
		}
		for _, f := range sub.fields {
			if f.inline == nil {
				f.inline = []int{i, f.num}
			} else {
				f.inline = append([]int{i}, f.inline...)
			}
			if err := s.add(f, st); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("option ,inline may only be used on a struct or map field")
	}
}

// isZeroer is yaml.v3's IsZeroer.
type isZeroer interface {
	IsZero() bool
}

// isZero decides whether an omitempty field is left out, as yaml.v3 does.
func isZero(v reflect.Value) bool {
	kind := v.Kind()
	if z, ok := v.Interface().(isZeroer); ok {
		if (kind == reflect.Pointer || kind == reflect.Interface) && v.IsNil() {
			return true
		}
		return z.IsZero()
	}
	switch kind {
	case reflect.String:
		return v.String() == ""
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Struct:
		return isZeroStruct(v)
	default:
		return false
	}
}

func isZeroStruct(v reflect.Value) bool {
	vt := v.Type()
	for i := v.NumField() - 1; i >= 0; i-- {
		if vt.Field(i).PkgPath != "" {
			continue // Private field.
		}
		if !isZero(v.Field(i)) {
			return false
		}
	}
	return true
}

// encodeBase64 encodes s as base64 that is broken up into multiple lines
// as appropriate for the resulting length.
func encodeBase64(s string) string {
	const lineLen = 70
	encLen := base64.StdEncoding.EncodedLen(len(s))
	lines := encLen/lineLen + 1
	buf := make([]byte, encLen*2+lines)
	in := buf[0:encLen]
	out := buf[encLen:]
	base64.StdEncoding.Encode(in, []byte(s))
	k := 0
	for i := 0; i < len(in); i += lineLen {
		j := i + lineLen
		if j > len(in) {
			j = len(in)
		}
		k += copy(out[k:], in[i:j])
		if lines > 1 {
			out[k] = '\n'
			k++
		}
	}
	return string(out[:k])
}

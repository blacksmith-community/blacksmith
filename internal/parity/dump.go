// Package parity holds the helpers that record what the broker makes of
// YAML today, so that a change of YAML library can be checked against the
// recordings.
package parity

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Dump renders a decoded value as one line per leaf, holding the leaf's
// path, its Go type, and its value, separated by tabs. It reads the value
// through reflection and uses no YAML library, so a recording stays
// readable when the libraries underneath it change. Map keys sort by their
// printed form, and a key that is not a string carries its type.
func Dump(value interface{}) string {
	var builder strings.Builder

	dumpValue(&builder, "$", reflect.ValueOf(value))

	return builder.String()
}

func dumpValue(builder *strings.Builder, path string, value reflect.Value) {
	if !value.IsValid() {
		writeLeaf(builder, path, "nil", "null")

		return
	}

	switch value.Kind() { //nolint:exhaustive // the remaining kinds are leaves
	case reflect.Interface, reflect.Ptr:
		if value.IsNil() {
			writeLeaf(builder, path, value.Type().String(), "null")

			return
		}

		dumpValue(builder, path, value.Elem())
	case reflect.Map:
		dumpMap(builder, path, value)
	case reflect.Slice, reflect.Array:
		dumpList(builder, path, value)
	case reflect.Struct:
		dumpStruct(builder, path, value)
	default:
		writeLeaf(builder, path, value.Type().String(), leafText(value))
	}
}

func dumpMap(builder *strings.Builder, path string, value reflect.Value) {
	if value.Len() == 0 {
		writeLeaf(builder, path, value.Type().String(), "{}")

		return
	}

	type entry struct {
		label string
		value reflect.Value
	}

	entries := make([]entry, 0, value.Len())

	iter := value.MapRange()
	for iter.Next() {
		key := iter.Key()
		for key.Kind() == reflect.Interface {
			key = key.Elem()
		}

		label := fmt.Sprintf("%v", key.Interface())
		if key.Kind() != reflect.String {
			label = fmt.Sprintf("%s(%s)", label, key.Type())
		}

		entries = append(entries, entry{label: label, value: iter.Value()})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].label < entries[j].label })

	for _, item := range entries {
		dumpValue(builder, path+"."+item.label, item.value)
	}
}

func dumpList(builder *strings.Builder, path string, value reflect.Value) {
	if value.Kind() == reflect.Slice && value.IsNil() {
		writeLeaf(builder, path, value.Type().String(), "null")

		return
	}

	if value.Len() == 0 {
		writeLeaf(builder, path, value.Type().String(), "[]")

		return
	}

	for index := range value.Len() {
		dumpValue(builder, fmt.Sprintf("%s[%d]", path, index), value.Index(index))
	}
}

func dumpStruct(builder *strings.Builder, path string, value reflect.Value) {
	if stamp, ok := value.Interface().(time.Time); ok {
		writeLeaf(builder, path, value.Type().String(), stamp.UTC().Format(time.RFC3339Nano))

		return
	}

	for index := range value.NumField() {
		field := value.Type().Field(index)
		if !field.IsExported() {
			continue
		}

		dumpValue(builder, path+"."+field.Name, value.Field(index))
	}
}

func leafText(value reflect.Value) string {
	if value.Kind() == reflect.String {
		return fmt.Sprintf("%q", value.String())
	}

	return fmt.Sprintf("%v", value.Interface())
}

func writeLeaf(builder *strings.Builder, path, typeName, text string) {
	builder.WriteString(path)
	builder.WriteByte('\t')
	builder.WriteString(typeName)
	builder.WriteByte('\t')
	builder.WriteString(text)
	builder.WriteByte('\n')
}

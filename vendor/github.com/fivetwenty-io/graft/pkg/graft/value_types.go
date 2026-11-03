package graft

import (
	"fmt"
	"math"
)

// ValueType string constants.
const (
	valueTypeNil     = "nil"
	valueTypeString  = "string"
	valueTypeInt     = "int"
	valueTypeBool    = "bool"
	valueTypeMap     = "map"
	valueTypeUnknown = "unknown"
)

// Value represents a type-safe value that can hold different types
// This provides better type safety than interface{} while maintaining flexibility.
type Value interface {
	// Type returns the underlying type of the value
	Type() ValueType

	// Raw returns the raw interface{} value for backward compatibility
	Raw() interface{}

	// String returns the string representation
	String() string

	// IsNil returns true if the value is nil
	IsNil() bool

	// AsString attempts to convert the value to a string
	AsString() (string, error)

	// AsInt attempts to convert the value to an int
	AsInt() (int, error)

	// AsInt64 attempts to convert the value to an int64
	AsInt64() (int64, error)

	// AsFloat64 attempts to convert the value to a float64
	AsFloat64() (float64, error)

	// AsBool attempts to convert the value to a bool
	AsBool() (bool, error)

	// AsSlice attempts to convert the value to a slice
	AsSlice() ([]interface{}, error)

	// AsMap attempts to convert the value to a map
	AsMap() (map[string]interface{}, error)
}

// ValueType represents the type of a Value.
type ValueType int

// ValueType constants.
const (
	// NilValue represents a nil value.
	NilValue ValueType = iota
	// StringValue represents a string value.
	StringValue
	// IntValue represents an int value.
	IntValue
	// Int64Value represents an int64 value.
	Int64Value
	// Float64Value represents a float64 value.
	Float64Value
	// BoolValue represents a bool value.
	BoolValue
	// SliceValue represents a slice value.
	SliceValue
	// MapValue represents a map value.
	MapValue
	// UnknownValue represents an unknown value type.
	UnknownValue
)

// String returns the string representation of the ValueType.
func (vt ValueType) String() string {
	switch vt {
	case NilValue:
		return valueTypeNil
	case StringValue:
		return valueTypeString
	case IntValue:
		return valueTypeInt
	case Int64Value:
		return "int64"
	case Float64Value:
		return "float64"
	case BoolValue:
		return valueTypeBool
	case SliceValue:
		return "slice"
	case MapValue:
		return valueTypeMap
	case UnknownValue:
		return valueTypeUnknown
	}
	return valueTypeUnknown
}

// valueImpl is the concrete implementation of Value.
type valueImpl struct {
	value interface{}
	vtype ValueType
}

// NewValue creates a new Value from an interface{}.
func NewValue(v interface{}) Value {
	if v == nil {
		return &valueImpl{value: nil, vtype: NilValue}
	}

	switch val := v.(type) {
	case string:
		return &valueImpl{value: val, vtype: StringValue}
	case int:
		return &valueImpl{value: val, vtype: IntValue}
	case int64:
		return &valueImpl{value: val, vtype: Int64Value}
	case float64:
		return &valueImpl{value: val, vtype: Float64Value}
	case bool:
		return &valueImpl{value: val, vtype: BoolValue}
	case []interface{}:
		return &valueImpl{value: val, vtype: SliceValue}
	case map[string]interface{}:
		return &valueImpl{value: val, vtype: MapValue}
	default:
		return &valueImpl{value: val, vtype: UnknownValue}
	}
}

// Type returns the ValueType.
func (v *valueImpl) Type() ValueType {
	return v.vtype
}

// Raw returns the raw value.
func (v *valueImpl) Raw() interface{} {
	return v.value
}

// String returns the string representation.
func (v *valueImpl) String() string {
	if v.value == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v", v.value)
}

// IsNil returns true if the value is nil.
func (v *valueImpl) IsNil() bool {
	return v.value == nil || v.vtype == NilValue
}

// AsString converts to string.
func (v *valueImpl) AsString() (string, error) {
	if v.IsNil() {
		return "", fmt.Errorf("cannot convert nil to string")
	}

	switch v.vtype {
	case StringValue:
		if s, ok := v.value.(string); ok {
			return s, nil
		}
	case IntValue:
		if i, ok := v.value.(int); ok {
			return fmt.Sprintf("%d", i), nil
		}
	case Int64Value:
		if i, ok := v.value.(int64); ok {
			return fmt.Sprintf("%d", i), nil
		}
	case Float64Value:
		if f, ok := v.value.(float64); ok {
			return fmt.Sprintf("%g", f), nil
		}
	case BoolValue:
		if b, ok := v.value.(bool); ok {
			return fmt.Sprintf("%t", b), nil
		}
	case NilValue, SliceValue, MapValue, UnknownValue:
		return fmt.Sprintf("%v", v.value), nil
	}
	return fmt.Sprintf("%v", v.value), nil
}

// AsInt converts to int.
func (v *valueImpl) AsInt() (int, error) {
	if v.IsNil() {
		return 0, fmt.Errorf("cannot convert nil to int")
	}

	switch v.vtype {
	case IntValue:
		if i, ok := v.value.(int); ok {
			return i, nil
		}
	case Int64Value:
		if val, ok := v.value.(int64); ok {
			if val > math.MaxInt32 || val < math.MinInt32 {
				return 0, fmt.Errorf("int64 value %d overflows int", val)
			}
			return int(val), nil
		}
	case Float64Value:
		if val, ok := v.value.(float64); ok {
			if val != float64(int(val)) {
				return 0, fmt.Errorf("float64 value %g is not an integer", val)
			}
			return int(val), nil
		}
	case NilValue, StringValue, BoolValue, SliceValue, MapValue, UnknownValue:
		return 0, fmt.Errorf("cannot convert %s to int", v.vtype)
	}
	return 0, fmt.Errorf("cannot convert %s to int", v.vtype)
}

// AsInt64 converts to int64.
func (v *valueImpl) AsInt64() (int64, error) {
	if v.IsNil() {
		return 0, fmt.Errorf("cannot convert nil to int64")
	}

	switch v.vtype {
	case IntValue:
		if i, ok := v.value.(int); ok {
			return int64(i), nil
		}
	case Int64Value:
		if i, ok := v.value.(int64); ok {
			return i, nil
		}
	case Float64Value:
		if val, ok := v.value.(float64); ok {
			if val != float64(int64(val)) {
				return 0, fmt.Errorf("float64 value %g is not an integer", val)
			}
			return int64(val), nil
		}
	case NilValue, StringValue, BoolValue, SliceValue, MapValue, UnknownValue:
		return 0, fmt.Errorf("cannot convert %s to int64", v.vtype)
	}
	return 0, fmt.Errorf("cannot convert %s to int64", v.vtype)
}

// AsFloat64 converts to float64.
func (v *valueImpl) AsFloat64() (float64, error) {
	if v.IsNil() {
		return 0, fmt.Errorf("cannot convert nil to float64")
	}

	switch v.vtype {
	case IntValue:
		if i, ok := v.value.(int); ok {
			return float64(i), nil
		}
	case Int64Value:
		if i, ok := v.value.(int64); ok {
			return float64(i), nil
		}
	case Float64Value:
		if f, ok := v.value.(float64); ok {
			return f, nil
		}
	case NilValue, StringValue, BoolValue, SliceValue, MapValue, UnknownValue:
		return 0, fmt.Errorf("cannot convert %s to float64", v.vtype)
	}
	return 0, fmt.Errorf("cannot convert %s to float64", v.vtype)
}

// AsBool converts to bool.
func (v *valueImpl) AsBool() (bool, error) {
	if v.IsNil() {
		return false, fmt.Errorf("cannot convert nil to bool")
	}

	switch v.vtype {
	case BoolValue:
		if b, ok := v.value.(bool); ok {
			return b, nil
		}
	case NilValue, StringValue, IntValue, Int64Value, Float64Value, SliceValue, MapValue, UnknownValue:
		return false, fmt.Errorf("cannot convert %s to bool", v.vtype)
	}
	return false, fmt.Errorf("cannot convert %s to bool", v.vtype)
}

// AsSlice converts to slice.
func (v *valueImpl) AsSlice() ([]interface{}, error) {
	if v.IsNil() {
		return nil, fmt.Errorf("cannot convert nil to slice")
	}

	switch v.vtype {
	case SliceValue:
		if s, ok := v.value.([]interface{}); ok {
			return s, nil
		}
	case NilValue, StringValue, IntValue, Int64Value, Float64Value, BoolValue, MapValue, UnknownValue:
		return nil, fmt.Errorf("cannot convert %s to slice", v.vtype)
	}
	return nil, fmt.Errorf("cannot convert %s to slice", v.vtype)
}

// AsMap converts to map.
func (v *valueImpl) AsMap() (map[string]interface{}, error) {
	if v.IsNil() {
		return nil, fmt.Errorf("cannot convert nil to map")
	}

	switch v.vtype {
	case MapValue:
		if m, ok := v.value.(map[string]interface{}); ok {
			return m, nil
		}
	case NilValue, StringValue, IntValue, Int64Value, Float64Value, BoolValue, SliceValue, UnknownValue:
		return nil, fmt.Errorf("cannot convert %s to map", v.vtype)
	}
	return nil, fmt.Errorf("cannot convert %s to map", v.vtype)
}

// TypedResponse is a more type-safe version of Response.
type TypedResponse struct {
	Type  Action
	Value Value
}

// NewTypedResponse creates a new TypedResponse.
func NewTypedResponse(action Action, value interface{}) *TypedResponse {
	return &TypedResponse{
		Type:  action,
		Value: NewValue(value),
	}
}

// ToLegacyResponse converts to the legacy Response format for backward compatibility.
func (tr *TypedResponse) ToLegacyResponse() *Response {
	return &Response{
		Type:  tr.Type,
		Value: tr.Value.Raw(),
	}
}

// NewResponseFromLegacy creates a TypedResponse from a legacy Response.
func NewResponseFromLegacy(r *Response) *TypedResponse {
	return &TypedResponse{
		Type:  r.Type,
		Value: NewValue(r.Value),
	}
}

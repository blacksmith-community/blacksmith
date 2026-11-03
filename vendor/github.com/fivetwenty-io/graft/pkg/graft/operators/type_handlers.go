package operators

import (
	"reflect"
)

// OperandType string constants.
const (
	operandTypeString  = "string"
	operandTypeMap     = "map"
	operandTypeList    = "list"
	operandTypeUnknown = "unknown"
)

// OperandType represents the type of an operand in graft expressions.
type OperandType int

// OperandType constants.
const (
	// TypeUnknown represents an unknown operand type.
	TypeUnknown OperandType = iota
	// TypeInt represents an integer operand.
	TypeInt
	// TypeFloat represents a float operand.
	TypeFloat
	// TypeString represents a string operand.
	TypeString
	// TypeBool represents a boolean operand.
	TypeBool
	// TypeMap represents a map operand.
	TypeMap
	// TypeList represents a list operand.
	TypeList
	// TypeNull represents a null operand.
	TypeNull
)

// String returns the string representation of an OperandType.
func (t OperandType) String() string {
	switch t {
	case TypeInt:
		return "int"
	case TypeFloat:
		return "float"
	case TypeString:
		return operandTypeString
	case TypeBool:
		return "bool"
	case TypeMap:
		return operandTypeMap
	case TypeList:
		return operandTypeList
	case TypeNull:
		return "null"
	case TypeUnknown:
		return operandTypeUnknown
	}
	return operandTypeUnknown
}

// TypeHandler defines operations that can be performed on specific type combinations.
type TypeHandler interface {
	// Arithmetic operations
	Add(a, b interface{}) (interface{}, error)
	Subtract(a, b interface{}) (interface{}, error)
	Multiply(a, b interface{}) (interface{}, error)
	Divide(a, b interface{}) (interface{}, error)
	Modulo(a, b interface{}) (interface{}, error)

	// Comparison operations
	Equal(a, b interface{}) (bool, error)
	NotEqual(a, b interface{}) (bool, error)
	Less(a, b interface{}) (bool, error)
	Greater(a, b interface{}) (bool, error)
	LessOrEqual(a, b interface{}) (bool, error)
	GreaterOrEqual(a, b interface{}) (bool, error)

	// Type checking
	CanHandle(aType, bType OperandType) bool
	Priority() int // Higher priority handlers are checked first
}

// TypeRegistry manages type handlers for different type combinations.
type TypeRegistry struct {
	handlers []TypeHandler
}

// NewTypeRegistry creates a new type registry.
func NewTypeRegistry() *TypeRegistry {
	return &TypeRegistry{
		handlers: make([]TypeHandler, 0),
	}
}

// Register adds a new type handler to the registry.
func (r *TypeRegistry) Register(handler TypeHandler) {
	r.handlers = append(r.handlers, handler)
	// Sort by priority (highest first)
	for i := len(r.handlers) - 1; i > 0; i-- {
		if r.handlers[i].Priority() > r.handlers[i-1].Priority() {
			r.handlers[i], r.handlers[i-1] = r.handlers[i-1], r.handlers[i]
		} else {
			break
		}
	}
}

// FindHandler finds the appropriate handler for the given operand types.
func (r *TypeRegistry) FindHandler(aType, bType OperandType) TypeHandler {
	for _, handler := range r.handlers {
		if handler.CanHandle(aType, bType) {
			return handler
		}
	}
	return nil
}

// GetOperandType determines the OperandType of a given value.
func GetOperandType(val interface{}) OperandType {
	if val == nil {
		return TypeNull
	}

	switch v := val.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return TypeInt
	case float32, float64:
		return TypeFloat
	case string:
		return TypeString
	case bool:
		return TypeBool
	case map[string]interface{}:
		return TypeMap
	case []interface{}:
		return TypeList
	default:
		// Use reflection for other types
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return TypeInt
		case reflect.Float32, reflect.Float64:
			return TypeFloat
		case reflect.String:
			return TypeString
		case reflect.Bool:
			return TypeBool
		case reflect.Map:
			return TypeMap
		case reflect.Slice, reflect.Array:
			return TypeList
		case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
			reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer, reflect.Struct, reflect.UnsafePointer:
			return TypeUnknown
		}
		return TypeUnknown
	}
}

// TypePair represents a pair of operand types.
type TypePair struct {
	A, B OperandType
}

// BaseTypeHandler provides common functionality for type handlers.
type BaseTypeHandler struct {
	supportedTypes map[TypePair]bool
	priority       int
}

// NewBaseTypeHandler creates a new base type handler.
func NewBaseTypeHandler(priority int) *BaseTypeHandler {
	return &BaseTypeHandler{
		supportedTypes: make(map[TypePair]bool),
		priority:       priority,
	}
}

// AddSupportedTypes adds supported type combinations.
func (h *BaseTypeHandler) AddSupportedTypes(pairs ...TypePair) {
	for _, pair := range pairs {
		h.supportedTypes[pair] = true
		// Also add the reverse for commutative operations
		h.supportedTypes[TypePair{A: pair.B, B: pair.A}] = true
	}
}

// CanHandle checks if this handler can handle the given type combination.
func (h *BaseTypeHandler) CanHandle(aType, bType OperandType) bool {
	return h.supportedTypes[TypePair{A: aType, B: bType}]
}

// Priority returns the handler's priority.
func (h *BaseTypeHandler) Priority() int {
	return h.priority
}

// Note: NotImplementedError is defined in operator_utils.go

// Global type registry instance.
var globalTypeRegistry = NewTypeRegistry()

// GetGlobalTypeRegistry returns the global type registry.
func GetGlobalTypeRegistry() *TypeRegistry {
	return globalTypeRegistry
}

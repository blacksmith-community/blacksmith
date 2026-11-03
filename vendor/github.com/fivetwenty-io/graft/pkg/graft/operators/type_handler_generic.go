package operators

// primaryComparer is an interface for the three primary comparison operations.
// Handlers that implement this can derive the three secondary operations
// (NotEqual, LessOrEqual, GreaterOrEqual) automatically via DerivedComparisons.
type primaryComparer interface {
	Equal(a, b interface{}) (bool, error)
	Less(a, b interface{}) (bool, error)
	Greater(a, b interface{}) (bool, error)
}

// DerivedComparisons provides the three derived comparison methods (NotEqual,
// LessOrEqual, GreaterOrEqual) for any TypeHandler that already implements
// the primary comparisons (Equal, Less, Greater).
//
// Embed this struct in a handler and supply the handler's own primaryComparer
// via NewDerivedComparisons. The three derived methods delegate to the primary
// operations on the wrapped comparer, eliminating repeated boilerplate across
// all concrete type handlers.
//
// Generic type parameter T is constrained to primaryComparer so the compiler
// enforces that only valid handler types can be wrapped.
type DerivedComparisons[T primaryComparer] struct {
	comparer T
}

// NewDerivedComparisons creates a DerivedComparisons wrapper for the given handler.
func NewDerivedComparisons[T primaryComparer](h T) DerivedComparisons[T] {
	return DerivedComparisons[T]{comparer: h}
}

// NotEqual returns true when Equal returns false (and propagates errors).
func (d DerivedComparisons[T]) NotEqual(a, b interface{}) (bool, error) {
	equal, err := d.comparer.Equal(a, b)
	return !equal, err
}

// LessOrEqual returns true when Greater returns false (and propagates errors).
func (d DerivedComparisons[T]) LessOrEqual(a, b interface{}) (bool, error) {
	greater, err := d.comparer.Greater(a, b)
	return !greater, err
}

// GreaterOrEqual returns true when Less returns false (and propagates errors).
func (d DerivedComparisons[T]) GreaterOrEqual(a, b interface{}) (bool, error) {
	less, err := d.comparer.Less(a, b)
	return !less, err
}

package yaml

import "reflect"

// MapItem is one key and value of a MapSlice.
type MapItem struct {
	Key, Value any
}

// MapSlice is a mapping that keeps the order its items are in. Marshal
// writes the items in that order at any depth, whatever KeyOrder says,
// while the Go maps in its values still follow KeyOrder.
// Keys follow the same rules as map keys, so a string, a number, a bool,
// or nil is fine and a collection is refused. Two items whose keys write
// the same text are refused as well, because the result would hold a
// repeated key. Only the exact types MapSlice and *MapSlice are ordered
// mappings, and a named type built on MapSlice is an ordinary slice.
// Unmarshal refuses the type, so we decode into a Node when we need to
// keep key order on the way in.
type MapSlice []MapItem

var mapSliceType = reflect.TypeFor[MapSlice]()

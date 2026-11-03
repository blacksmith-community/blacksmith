package tree

import (
	"errors"
	"fmt"
	"strconv"
)

// Glob performs glob pattern matching on the cursor path.
//
//nolint:gocyclo // recursive glob matching with type handling is inherently complex
func (c *Cursor) Glob(tree interface{}) ([]*Cursor, error) {
	var resolver func(interface{}, []string, []string, int) ([]interface{}, error)
	resolver = func(o interface{}, here, path []string, pos int) ([]interface{}, error) {
		if pos == len(path) {
			return []interface{}{
				(&Cursor{Nodes: here}).Copy(),
			}, nil
		}

		paths := []interface{}{}
		k := path[pos]
		if k == "*" {
			switch oVal := o.(type) {
			case []interface{}:
				for i, v := range oVal {
					sub, err := resolver(v, append(here, fmt.Sprintf("%d", i)), path, pos+1)
					if err != nil {
						var notFound NotFoundError
						if !errors.As(err, &notFound) {
							return nil, err
						}
					}
					paths = append(paths, sub...)
				}

			case map[string]interface{}:
				for k, v := range oVal {
					sub, err := resolver(v, append(here, k), path, pos+1)
					if err != nil {
						var notFound NotFoundError
						if !errors.As(err, &notFound) {
							return nil, err
						}
					}
					paths = append(paths, sub...)
				}

			default:
				return nil, TypeMismatchError{
					Path:   path,
					Wanted: wantedContainer,
					Got:    gotScalar,
				}
			}
		} else {
			switch val := o.(type) {
			case []interface{}:
				if field, value, isPredicate := parsePredicateSegment(k); isPredicate {
					found, idx, ok := predicateFind(val, field, value)
					if !ok {
						return nil, NotFoundError{
							Path: path[0 : pos+1],
						}
					}
					return resolver(found, append(here, fmt.Sprintf("%d", idx)), path, pos+1)
				}

				i, err := strconv.ParseUint(k, 10, 0)
				if err == nil {
					// if k is an integer (in string form), go by index
					if i >= uint64(len(val)) {
						return nil, NotFoundError{
							Path: path[0 : pos+1],
						}
					}
					return resolver(val[i], append(here, k), path, pos+1)
				}

				// if k is a string, look for immediate map descendants who have
				//     'name', 'key' or 'id' fields matching k
				var found bool
				o, _, found = listFind(val, NameFields, k)
				if !found {
					return nil, NotFoundError{
						Path: path[0 : pos+1],
					}
				}
				return resolver(o, append(here, k), path, pos+1)

			case map[string]interface{}:
				v, ok := val[k]
				if !ok {
					return nil, NotFoundError{
						Path: path[0 : pos+1],
					}
				}
				return resolver(v, append(here, k), path, pos+1)

			default:
				return nil, TypeMismatchError{
					Path:   path[0:pos],
					Wanted: wantedContainer,
					Got:    gotScalar,
				}
			}
		}

		return paths, nil
	}

	path := append([]string{}, c.Nodes...)

	l, err := resolver(tree, []string{}, path, 0)
	if err != nil {
		return nil, err
	}

	cursors := []*Cursor{}
	for _, c := range l {
		cursor, ok := c.(*Cursor)
		if !ok {
			continue
		}
		cursors = append(cursors, cursor)
	}
	return cursors, nil
}

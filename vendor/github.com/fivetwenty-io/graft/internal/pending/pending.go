// Package pending records which operators of an evaluation phase have
// not run yet, so an operator that reads the tree can tell an operator
// still waiting to run from text that only looks like one. The evaluator
// keeps the record. The operators package reads it through a hook that
// pkg/graft installs, so the evaluator exports no field for it.
package pending

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/fivetwenty-io/graft/internal/literal"
)

// Record holds the operators of one phase that are scheduled and have
// not run yet. It keys each by its canonical path, and also by the slot
// it sits in, which is the entry of a map or a list that holds it. A
// path that reaches that entry through another grab's value names a
// different path but the same slot. It is safe for concurrent use, and
// a nil *Record holds nothing.
type Record struct {
	mu    sync.Mutex
	at    map[string]string
	slots map[slot]string
	slot  map[string]slot
	// pins holds every container a slot names, so that its address
	// stays its own while the record holds the slot.
	pins []interface{}
}

// slot names one entry of a map or a list by the container's identity:
// the map's address and the entry's key, or the address of the list's
// element.
type slot struct {
	p uintptr
	k string
}

// slotIn returns the slot of the entry key of the container parent,
// and false when parent is not a map or a list or holds no such entry.
func slotIn(parent interface{}, key string) (slot, bool) {
	switch c := parent.(type) {
	case map[string]interface{}:
		if c == nil {
			return slot{}, false
		}
		return slot{p: reflect.ValueOf(c).Pointer(), k: key}, true
	case []interface{}:
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(c) {
			return slot{}, false
		}
		return slot{p: reflect.ValueOf(&c[i]).Pointer()}, true
	}
	return slot{}, false
}

// NewRecord returns an empty Record.
func NewRecord() *Record {
	return &Record{at: map[string]string{}, slots: map[slot]string{}, slot: map[string]slot{}}
}

// Add records that the operator at the canonical path nodes, written in
// the document at where, has not run yet. parent is the container that
// holds it, the value at nodes without its last node, or nil when that
// is not known.
func (r *Record) Add(nodes []string, where string, parent interface{}) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(nodes)
	r.at[k] = where
	if len(nodes) == 0 {
		return
	}
	if s, ok := slotIn(parent, nodes[len(nodes)-1]); ok {
		r.slots[s] = where
		r.slot[k] = s
		r.pins = append(r.pins, parent)
	}
}

// Done records that the operator at the canonical path nodes has run,
// whether or not it succeeded.
func (r *Record) Done(nodes []string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(nodes)
	delete(r.at, k)
	if s, ok := r.slot[k]; ok {
		delete(r.slots, s)
		delete(r.slot, k)
	}
}

// At reports whether an operator at the canonical path nodes has not run
// yet, and if so the path its document wrote it at.
func (r *Record) At(nodes []string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	where, ok := r.at[key(nodes)]
	return where, ok
}

// In reports whether the entry key of the container parent holds an
// operator that has not run yet, and if so the path its document wrote
// it at.
func (r *Record) In(parent interface{}, key string) (string, bool) {
	if r == nil {
		return "", false
	}
	s, ok := slotIn(parent, key)
	if !ok {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	where, ok := r.slots[s]
	return where, ok
}

// Within reports whether any entry of the value val, at any depth,
// holds an operator that has not run yet, and if so the path its
// document wrote the first one at. Map keys are visited in sorted order,
// so the answer does not depend on map order. A literal is opaque and is
// not looked into, and a container is visited once even when it holds
// itself.
func (r *Record) Within(val interface{}) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.slots) == 0 {
		return "", false
	}
	return r.within(val, map[uintptr]bool{})
}

// within is Within with r.mu held, and seen holding the containers
// visited so far.
func (r *Record) within(val interface{}, seen map[uintptr]bool) (string, bool) {
	switch c := val.(type) {
	case map[string]interface{}:
		if c == nil || seen[reflect.ValueOf(c).Pointer()] {
			return "", false
		}
		seen[reflect.ValueOf(c).Pointer()] = true
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if where, ok := r.slots[slot{p: reflect.ValueOf(c).Pointer(), k: k}]; ok {
				return where, true
			}
			if where, ok := r.within(c[k], seen); ok {
				return where, true
			}
		}
	case []interface{}:
		if len(c) == 0 || seen[reflect.ValueOf(&c[0]).Pointer()] {
			return "", false
		}
		seen[reflect.ValueOf(&c[0]).Pointer()] = true
		for i := range c {
			if where, ok := r.slots[slot{p: reflect.ValueOf(&c[i]).Pointer()}]; ok {
				return where, true
			}
			if where, ok := r.within(c[i], seen); ok {
				return where, true
			}
		}
	}
	return "", false
}

// key encodes nodes so that no two paths share a key, even when a node
// holds a dot.
func key(nodes []string) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(strconv.Itoa(len(n)))
		b.WriteByte(':')
		b.WriteString(n)
	}
	return b.String()
}

// lookup returns the Record of the evaluator ev, or nil.
var lookup func(ev any) *Record

// SetLookup installs the function For calls. pkg/graft calls it once,
// from an init function.
func SetLookup(f func(ev any) *Record) {
	lookup = f
}

// For returns the Record of the evaluator ev, or nil when ev keeps none,
// as when it runs operators outside a phase.
func For(ev any) *Record {
	if lookup == nil {
		return nil
	}
	return lookup(ev)
}

// outerArgs reports whether args are the arguments of the operator
// call the evaluator ev runs at the top level.
var outerArgs func(ev any, args any) bool

// SetOuterArgs installs the function IsOuterCall calls. pkg/graft calls
// it once, from an init function.
func SetOuterArgs(f func(ev any, args any) bool) {
	outerArgs = f
}

// IsOuterCall reports whether args, the arguments an operator's Run was
// given, are those of the call the evaluator ev runs at the top level,
// whose value the evaluator writes to the tree. It is false for a call
// nested in another call's arguments, whose value another operator
// reads straight away, and false when no hook is installed.
func IsOuterCall(ev any, args any) bool {
	if outerArgs == nil {
		return false
	}
	return outerArgs(ev, args)
}

// ErrNotEvaluated is the error every NotEvaluatedError matches with
// errors.Is.
var ErrNotEvaluated = errors.New("a grab read a path whose operator has not run yet")

// NotEvaluatedError is the error a grab returns when the path it reads
// holds an operator that has not run yet, and the dependency graph could
// not order the grab after it. Its message names the target and quotes
// no value. It does not name the grab, because the path error the
// evaluator wraps it in does.
type NotEvaluatedError struct {
	// Grab is the path of the operator call that ran the grab, without
	// a leading "$.".
	Grab string
	// Target is the path of the operator the grab reached, without a
	// leading "$.".
	Target string
}

func (e *NotEvaluatedError) Error() string {
	return fmt.Sprintf("(( grab )) reads $.%s before the operator there has run, and graft could not order the grab after it", e.Target)
}

// Is reports whether target is ErrNotEvaluated.
func (e *NotEvaluatedError) Is(target error) bool {
	return target == ErrNotEvaluated
}

// ErrUnknownOperator is matched by the error for a call to an operator
// that is not registered. Its text is "unknown operator", and the error
// that wraps it adds the operator's name.
var ErrUnknownOperator = errors.New("unknown operator")

// UnknownOperator returns the error for a call to the operator name,
// which is not registered. It reads "unknown operator: <name>" and
// matches ErrUnknownOperator.
func UnknownOperator(name string) error {
	return fmt.Errorf("%w: %s", ErrUnknownOperator, name)
}

// StopsFallback reports whether err must not be answered by trying the
// other side of a || fallback: a refused literal, a grab of an
// operator that has not run, whose fallback would hide a wrong order,
// or a call to an operator that does not exist, whose fallback would
// hide a mistake in the template.
func StopsFallback(err error) bool {
	return literal.IsRefusal(err) || errors.Is(err, ErrNotEvaluated) ||
		errors.Is(err, ErrUnknownOperator)
}

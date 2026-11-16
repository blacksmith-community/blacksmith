package yaml

import (
	"math"
	"math/bits"
	"reflect"
	"slices"
	"strings"

	"github.com/goccy/go-yaml/ast"
)

// reachesInlineMap reports whether a value of type t holds, at any
// depth, a struct field tagged ",inline" whose type is a map.
//
// goccy fills such a field from a Go map of the struct's keys, so when
// several of its values are bad, which one the error names changes from
// run to run.
func reachesInlineMap(t reflect.Type) bool {
	w := typeWalk{leaf: inlinesMap, field: decodesField, seen: make(map[reflect.Type]bool)}
	return w.reaches(t)
}

// inlinesMap reports whether t is a struct with an ,inline field that
// goccy decodes and whose type is a map or a pointer to one.
func inlinesMap(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !decodesField(f) {
			continue
		}
		_, opts, _ := strings.Cut(fieldTag(f), ",")
		if !inlineOption(opts) {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Map {
			return true
		}
	}
	return false
}

// earliestTypeError returns the error with the smallest line and column
// among the errors goccy can report for body decoded into a value of
// v's type, given first, the error the original decode returned. It
// decodes copies of body into new values of that type, so body and v
// are never changed. own holds the scalars asWritten found in body, so
// each error goes through the same steps as the first one did.
//
// The search works on units, which are the pairs of a mapping and the
// items of a sequence. A unit starts at the earliest position of any
// node under it, aliases included, since alias expansion puts the
// anchor's node in the alias's slot and a merge folds the anchor's pairs
// into the mapping that names them. Units are ordered by where they
// start and then by their own token, which is a pair's key or the item
// itself, so units that share a start, as pairs whose values alias one
// anchor do, still fall in a strict order. A prefix of body keeps the
// units up to a point in that order. The prefix that keeps every unit
// starting at or before a position holds every node at or before it,
// with the same Go type to fill as in body. Whether a value fits
// depends only on the value and that type, so whether a prefix fails
// can only go from false to true as the prefix grows.
//
// Each round searches by halves for the shortest failing prefix that
// keeps no unit starting after the best error so far. The units before
// that prefix's last one hold no bad value, so its error is the first
// bad value in unit order, and it does not depend on the order goccy
// happens to fill an inline map in. The first round searches the whole
// body, and each later round first drops every innermost unit that
// holds the last error's position. When the longest prefix fails no
// more, nothing at or before the best error is left, and the best error
// is the earliest. A round costs one decode and one for each halving,
// and an input reaches the answer in two rounds unless aliases put
// values out of document order. The rounds are capped at a small
// multiple of the bits in the unit count, and past the cap the best
// error found is returned. Since every round's search is the same on
// every run, so is that error.
//
// The drop removes a shared node's unit at every site the node fills,
// since an error names a position and not a site. When one node fills
// two sites as two different Go types, the unit dropped at one site can
// hold a bad key that sits before the error, and that key is lost.
//
// A caller's own unmarshal method can panic when a prefix hands it a
// mapping with keys missing. Such a panic stops the search, and the
// best error found so far is returned. Every error the search returns
// is one goccy reported, so a stopped search names a real bad value,
// only perhaps not the earliest.
func earliestTypeError(body ast.Node, v any, opts DecodeOptions, own ownScalars, first *DecodeError) *DecodeError {
	s := prefixSearch{target: reflect.TypeOf(v), opts: opts, own: own}
	best, units, stopped := s.firstFailure(body, errorSrcPos(first))
	if !reportable(best) {
		return first
	}
	last := best
	limit := 4 + 2*bits.Len(uint(units))
	for round := 1; round < limit && !stopped; round++ {
		next, dropped := unitDrop{at: errorSrcPos(last), memo: make(map[ast.Node]dropped)}.node(body)
		if !dropped {
			break
		}
		body = next
		var found *DecodeError
		found, _, stopped = s.firstFailure(body, errorSrcPos(best))
		if !reportable(found) {
			break
		}
		last = found
		if earlier(last, best) {
			best = last
		}
	}
	return best
}

// reportable reports whether e is a type error with a position, which
// is the only kind the search compares.
func reportable(e *DecodeError) bool {
	return e != nil && e.Kind == ErrorKindType && e.Line > 0
}

// earlier reports whether a comes before b by line, then column, then
// message.
func earlier(a, b *DecodeError) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	if a.Column != b.Column {
		return a.Column < b.Column
	}
	return a.Msg < b.Msg
}

// srcPos is a line and column in the input. A node with no position
// sits at noSrcPos, after every other.
type srcPos struct {
	line, column int
}

var noSrcPos = srcPos{line: math.MaxInt, column: math.MaxInt}

// after reports whether p comes after q.
func (p srcPos) after(q srcPos) bool {
	return p.line > q.line || p.line == q.line && p.column > q.column
}

// errorSrcPos returns where e points.
func errorSrcPos(e *DecodeError) srcPos {
	return srcPos{line: e.Line, column: e.Column}
}

// srcPosOf returns where n's token sits.
func srcPosOf(n ast.Node) srcPos {
	if tk := n.GetToken(); tk != nil && tk.Position != nil {
		return srcPos{line: tk.Position.Line, column: tk.Position.Column}
	}
	return noSrcPos
}

// prefixSearch decodes prefixes of a body into new values of target.
type prefixSearch struct {
	target reflect.Type
	opts   DecodeOptions
	own    ownScalars
}

// firstFailure returns the error of the shortest failing prefix of body
// that keeps no unit starting after bound, or nil when none fails. It
// also returns how many units body holds, and whether a decode panicked.
// A panic ends the search, which then returns the error of the shortest
// failing prefix it has decoded, or nil when it has none or when
// placing that error panics.
func (s prefixSearch) firstFailure(body ast.Node, bound srcPos) (found *DecodeError, units int, stopped bool) {
	st := &unitStarts{memo: make(map[ast.Node]srcPos), seen: make(map[ast.Node]bool)}
	st.collect(body)
	units = len(st.all)
	ends := st.upTo(bound)
	if len(ends) == 0 {
		return nil, units, false
	}
	failed := st.prefix(body, ends[len(ends)-1])
	panicked, err := s.decode(failed)
	if panicked || err == nil {
		return nil, units, panicked
	}
	lo, hi := 0, len(ends)-1
	for lo < hi {
		mid := lo + (hi-lo)/2
		b := st.prefix(body, ends[mid])
		panicked, got := s.decode(b)
		if panicked {
			stopped = true
			break
		}
		if got != nil {
			hi, err, failed = mid, got, b
		} else {
			lo = mid + 1
		}
	}
	found, panicked = s.place(err, failed)
	if panicked {
		return nil, units, true
	}
	return found, units, stopped
}

// place turns err, which decoding body returned, into the error the
// caller sees, by the same steps as the first error. locate can decode
// body again, so place recovers a panic and reports it as decode does.
func (s prefixSearch) place(err error, body ast.Node) (found *DecodeError, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			found, panicked = nil, true
		}
	}()
	return translate(s.own.locate(locate(err, body, s.target), s.target.Elem())), false
}

// decode decodes body into a new value of target. It recovers any panic
// and reports it, since a caller's own unmarshal method may not expect
// a mapping that the search has cut short.
func (s prefixSearch) decode(body ast.Node) (panicked bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicked, err = true, nil
		}
	}()
	return false, decodeBody(body, reflect.New(s.target.Elem()).Interface(), s.opts)
}

// unitKey places a unit in the search's order, by where its subtree
// starts and then by its own token.
type unitKey struct {
	start, own srcPos
}

// compare orders k and o by start, then by own token.
func (k unitKey) compare(o unitKey) int {
	switch {
	case k == o:
		return 0
	case k.start == o.start && o.own.after(k.own), o.start.after(k.start):
		return -1
	default:
		return 1
	}
}

// unitStarts computes where each node's subtree starts, once for each
// node, since alias expansion shares subtrees.
type unitStarts struct {
	memo map[ast.Node]srcPos
	seen map[ast.Node]bool
	all  []unitKey
}

// pairKey returns the key of the unit a pair makes.
func (st *unitStarts) pairKey(pair *ast.MappingValueNode) unitKey {
	return unitKey{start: st.start(pair), own: srcPosOf(pair.Key)}
}

// itemKey returns the key of the unit a sequence item makes.
func (st *unitStarts) itemKey(item ast.Node) unitKey {
	return unitKey{start: st.start(item), own: srcPosOf(item)}
}

// start returns the earliest position of n or any node under it.
func (st *unitStarts) start(n ast.Node) srcPos {
	if n == nil {
		return noSrcPos
	}
	if got, ok := st.memo[n]; ok {
		return got
	}
	got := srcPosOf(n)
	earliest := func(c ast.Node) {
		if c := st.start(c); got.after(c) {
			got = c
		}
	}
	switch n := n.(type) {
	case *ast.MappingNode:
		for _, pair := range n.Values {
			earliest(pair)
		}
	case *ast.MappingValueNode:
		earliest(n.Key)
		earliest(n.Value)
	case *ast.SequenceNode:
		for _, item := range n.Values {
			earliest(item)
		}
	default:
		if child, rebuild := inner(n); rebuild != nil {
			earliest(child)
		}
	}
	st.memo[n] = got
	return got
}

// collect records the start of every unit under n.
func (st *unitStarts) collect(n ast.Node) {
	if n == nil || st.seen[n] {
		return
	}
	st.seen[n] = true
	switch n := n.(type) {
	case *ast.MappingNode:
		for _, pair := range n.Values {
			st.all = append(st.all, st.pairKey(pair))
			st.collect(pair.Key)
			st.collect(pair.Value)
		}
	case *ast.MappingValueNode:
		st.collect(n.Key)
		st.collect(n.Value)
	case *ast.SequenceNode:
		for _, item := range n.Values {
			st.all = append(st.all, st.itemKey(item))
			st.collect(item)
		}
	default:
		if child, rebuild := inner(n); rebuild != nil {
			st.collect(child)
		}
	}
}

// upTo returns the distinct keys of the units that start no later than
// bound, in order.
func (st *unitStarts) upTo(bound srcPos) []unitKey {
	out := make([]unitKey, 0, len(st.all))
	for _, k := range st.all {
		if !k.start.after(bound) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, unitKey.compare)
	return slices.Compact(out)
}

// prefix returns body with only the units whose keys come no later than
// end. It copies each node on the path to one it changes and shares
// every other subtree.
func (st *unitStarts) prefix(body ast.Node, end unitKey) ast.Node {
	pr := unitPrefix{starts: st, end: end, memo: make(map[ast.Node]ast.Node)}
	return pr.node(body)
}

// unitPrefix cuts a body down to the units whose keys come no later
// than end.
type unitPrefix struct {
	starts *unitStarts
	end    unitKey
	memo   map[ast.Node]ast.Node
}

func (pr unitPrefix) node(n ast.Node) ast.Node {
	if n == nil {
		return nil
	}
	if got, ok := pr.memo[n]; ok {
		return got
	}
	out := n
	switch n := n.(type) {
	case *ast.MappingNode:
		out = pr.mapping(n)
	case *ast.SequenceNode:
		out = pr.sequence(n)
	case *ast.MappingValueNode:
		if got := pr.node(n.Value); got != n.Value {
			c := *n
			c.Value = got
			out = &c
		}
	default:
		if child, rebuild := inner(n); rebuild != nil {
			if got := pr.node(child); got != child {
				out = rebuild(got)
			}
		}
	}
	pr.memo[n] = out
	return out
}

func (pr unitPrefix) mapping(m *ast.MappingNode) ast.Node {
	kept := make([]*ast.MappingValueNode, 0, len(m.Values))
	changed := false
	for _, pair := range m.Values {
		if pr.starts.pairKey(pair).compare(pr.end) > 0 {
			changed = true
			continue
		}
		got := asPair(pr.node(pair), pair)
		if got != pair {
			changed = true
		}
		kept = append(kept, got)
	}
	if !changed {
		return m
	}
	c := *m
	c.Values = kept
	return &c
}

func (pr unitPrefix) sequence(s *ast.SequenceNode) ast.Node {
	kept := make([]ast.Node, 0, len(s.Values))
	changed := false
	for _, item := range s.Values {
		if pr.starts.itemKey(item).compare(pr.end) > 0 {
			changed = true
			continue
		}
		got := pr.node(item)
		if got != item {
			changed = true
		}
		kept = append(kept, got)
	}
	if !changed {
		return s
	}
	c := *s
	c.Values = kept
	return &c
}

// dropped is what unitDrop made of a node: the node without the units
// it removed, and whether a node at the position is left in it outside
// any unit it removed.
type dropped struct {
	out   ast.Node
	found bool
}

// unitDrop removes from a body every innermost unit that holds a node
// at a position, whether in a key or a value. The token of a block
// mapping or sequence is the token of its first entry's ':' or '-', so
// a container whose own token sits at the position is the node found,
// and its own unit goes. It copies each node on the path to one it
// changes and shares every other subtree.
type unitDrop struct {
	at   srcPos
	memo map[ast.Node]dropped
}

// node returns n without the units that hold the position, and whether
// it removed any.
func (d unitDrop) node(n ast.Node) (ast.Node, bool) {
	got := d.walk(n)
	return got.out, got.out != n
}

func (d unitDrop) walk(n ast.Node) dropped {
	if n == nil {
		return dropped{}
	}
	if got, ok := d.memo[n]; ok {
		return got
	}
	var got dropped
	switch n := n.(type) {
	case *ast.MappingNode:
		got = d.mapping(n)
	case *ast.SequenceNode:
		got = d.sequence(n)
	case *ast.MappingValueNode:
		got = d.pair(n)
	default:
		got = dropped{out: n, found: srcPosOf(n) == d.at}
		if child, rebuild := inner(n); rebuild != nil {
			c := d.walk(child)
			if c.out != child {
				got.out = rebuild(c.out)
			}
			got.found = got.found || c.found
		}
	}
	d.memo[n] = got
	return got
}

// pair walks the key and the value of a pair. The pair's own token is
// its ':', which a block mapping shares, so it is never the node found.
func (d unitDrop) pair(n *ast.MappingValueNode) dropped {
	key, val := d.walk(n.Key), d.walk(n.Value)
	got := dropped{out: n, found: key.found || val.found}
	if key.out != n.Key || val.out != n.Value {
		c := *n
		if k, ok := key.out.(ast.MapKeyNode); ok {
			c.Key = k
		}
		c.Value = val.out
		got.out = &c
	}
	return got
}

func (d unitDrop) mapping(m *ast.MappingNode) dropped {
	kept := make([]*ast.MappingValueNode, 0, len(m.Values))
	changed := false
	for _, pair := range m.Values {
		got := d.walk(pair)
		if got.found {
			changed = true
			continue
		}
		out := asPair(got.out, pair)
		if out != pair {
			changed = true
		}
		kept = append(kept, out)
	}
	got := dropped{out: m, found: srcPosOf(m) == d.at}
	if changed {
		c := *m
		c.Values = kept
		got.out = &c
	}
	return got
}

func (d unitDrop) sequence(s *ast.SequenceNode) dropped {
	kept := make([]ast.Node, 0, len(s.Values))
	changed := false
	for _, item := range s.Values {
		got := d.walk(item)
		if got.found {
			changed = true
			continue
		}
		if got.out != item {
			changed = true
		}
		kept = append(kept, got.out)
	}
	got := dropped{out: s, found: srcPosOf(s) == d.at}
	if changed {
		c := *s
		c.Values = kept
		got.out = &c
	}
	return got
}

// asPair returns n, the copy of pair that a walk made, as a pair, or
// pair itself when n is not one, which a walk never returns.
func asPair(n ast.Node, pair *ast.MappingValueNode) *ast.MappingValueNode {
	if got, ok := n.(*ast.MappingValueNode); ok {
		return got
	}
	return pair
}

// inner returns the one node that n holds, when n is a document, a tag,
// or an anchor, and a function that returns a copy of n holding another
// node in its place. For any other node both are nil.
func inner(n ast.Node) (ast.Node, func(ast.Node) ast.Node) {
	switch n := n.(type) {
	case *ast.DocumentNode:
		return n.Body, func(v ast.Node) ast.Node {
			c := *n
			c.Body = v
			return &c
		}
	case *ast.TagNode:
		return n.Value, func(v ast.Node) ast.Node {
			c := *n
			c.Value = v
			return &c
		}
	case *ast.AnchorNode:
		return n.Value, func(v ast.Node) ast.Node {
			c := *n
			c.Value = v
			return &c
		}
	default:
		return nil, nil
	}
}

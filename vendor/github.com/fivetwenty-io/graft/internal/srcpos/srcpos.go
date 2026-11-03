// Package srcpos maps a graft document path to the source line of the
// operator expression written at that path.
//
// It exists for one purpose: when a merge fails with an operator
// data-flow cycle, the error needs to name the file and line of every
// operator on the cycle. Building an index is therefore a lazy,
// error-path-only cost - nothing here runs on a successful merge.
//
// Build indexes only scalars whose text contains "((" - candidate
// operator calls - so an index over an ordinary data file is empty and
// nearly free.
package srcpos

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml/ast"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
)

// nameFields mirrors tree.NameFields (pkg/graft/tree/types.go). It is
// duplicated rather than imported to keep this package free of any
// pkg/graft dependency beyond the leaf interfaces package. Keep the two
// lists in sync.
var nameFields = []string{"name", "key", "id"}

// Entry is one indexed operator expression.
type Entry struct {
	// Path is the canonical graft path, e.g. "jobs.0.cmd".
	Path string
	// Alias is the name-keyed form of Path, e.g. "jobs.web.cmd", or ""
	// when no ancestor list element carries a name field.
	Alias string
	// Expr is the operator text, space-trimmed to match Opcall.src.
	Expr string
	// Pos is where Expr was written.
	Pos interfaces.Position
}

// Index holds every operator expression found in one source.
type Index struct {
	name    string
	byPath  map[string]Entry
	byAlias map[string]Entry
	byExpr  map[string][]Entry
}

// Build indexes every operator expression in data, which MUST already
// have been through the engine's parse-time rewrites (see
// DefaultEngine.ParseYAML). It never returns an error: a parse failure
// yields an empty Index, because the caller is already reporting a
// different error and must not be derailed by a second one.
func Build(name string, data []byte) *Index {
	idx := &Index{
		name:    name,
		byPath:  make(map[string]Entry),
		byAlias: make(map[string]Entry),
		byExpr:  make(map[string][]Entry),
	}
	if len(data) == 0 {
		return idx
	}

	file, err := yamlnode.ParseBytes(data, 0)
	if err != nil || file == nil {
		return idx
	}

	// Only the first document is indexed. ParseYAML11CompatAware
	// converts the body yamlnode.FirstBody returns and nothing else
	// (pkg/graft/yaml_compat.go), so later documents contribute no node
	// to the merged tree. Indexing them would let an expression the merge
	// never evaluated claim a cycle node's path, and would inflate the
	// per-expression counts the expression fallback relies on. The
	// directives before a "---" are a document of their own to goccy,
	// and FirstBody skips them, as the merge does.
	body := yamlnode.FirstBody(file)
	if body == nil {
		return idx
	}

	names := make(map[string]string)
	c := &collector{idx: idx, name: name, names: names, keys: make(map[ast.Node]bool)}
	ast.Walk(c, body)

	idx.buildAliases(names)
	return idx
}

// Lookup returns the entry for one graft path, consulting the exact-path
// map first and the name-keyed alias map second.
func (i *Index) Lookup(path string) (Entry, bool) {
	if i == nil {
		return Entry{}, false
	}
	if e, ok := i.byPath[path]; ok {
		return e, true
	}
	if e, ok := i.byAlias[path]; ok {
		return e, true
	}
	return Entry{}, false
}

// CountExpr returns how many entries in this index carry expr. Summing
// it across every source is how a caller decides whether an expression
// is unique across the union before relying on it.
func (i *Index) CountExpr(expr string) int {
	if i == nil {
		return 0
	}
	return len(i.byExpr[expr])
}

// Exprs returns a count of entries per normalized expression. It builds
// a fresh map over the whole index, so ask CountExpr instead when only
// one expression's count is wanted.
func (i *Index) Exprs() map[string]int {
	out := make(map[string]int)
	if i == nil {
		return out
	}
	for expr, list := range i.byExpr {
		out[expr] = len(list)
	}
	return out
}

// ByExpr returns the single entry carrying expr, if this index has
// exactly one.
func (i *Index) ByExpr(expr string) (Entry, bool) {
	if i == nil {
		return Entry{}, false
	}
	list := i.byExpr[expr]
	if len(list) != 1 {
		return Entry{}, false
	}
	return list[0], true
}

type collector struct {
	idx   *Index
	name  string
	names map[string]string // list-element path -> name-field value
	keys  map[ast.Node]bool // mapping-key nodes, never indexed
}

func (c *collector) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.MappingValueNode:
		// Record the key node so the StringNode case below can skip it
		// by identity. A mapping key shares its value's path, so an
		// operator-shaped key would otherwise overwrite the value's
		// entry with the key's own text and position.
		if n.Key != nil {
			c.keys[n.Key] = true
		}
	case *ast.StringNode:
		c.visitString(n)
	}
	return c
}

func (c *collector) visitString(sn *ast.StringNode) {
	if c.keys[sn] {
		return
	}
	path, ok := convertPath(sn.GetPath())
	if !ok {
		return
	}

	// Name fields feed the alias map whether or not they are operators.
	if base, field, ok := splitLast(path); ok && isNameField(field) && isIndexSegment(base) {
		if _, exists := c.names[base]; !exists {
			c.names[base] = sn.Value
		}
	}

	// Selection is by content, not by node role: a bare list element
	// such as "- (( grab meta.a ))" is a StringNode at $.list[0] and is
	// not a mapping value, so a role-based filter would drop it.
	if !strings.Contains(sn.Value, "((") {
		return
	}
	tok := sn.GetToken()
	if tok == nil || tok.Position == nil {
		return
	}

	// Opcall.src is space-trimmed (pkg/graft/parser.go), while a block
	// scalar's value carries a trailing newline. Trim so the two forms
	// compare equal.
	e := Entry{
		Path: path,
		Expr: strings.TrimSpace(sn.Value),
		Pos: interfaces.Position{
			Line:   tok.Position.Line,
			Column: tok.Position.Column,
			Offset: tok.Position.Offset,
			File:   c.name,
		},
	}
	c.idx.byPath[path] = e
	c.idx.byExpr[e.Expr] = append(c.idx.byExpr[e.Expr], e)
}

// buildAliases records the name-keyed form of every indexed path, so
// attribution survives list index drift when a later file appends,
// inserts, or merges on name.
//
// One name may be carried by several list elements, in which case their
// operators all want the same alias and the first to claim it wins.
// Ranging over byPath would hand that decision to Go's randomized map
// iteration order, so the paths are walked in document order instead -
// by line, then column, then path - and the entry written earliest in
// the file owns the alias on every build.
//
// Document order is what the tie must break on. Sorting the paths
// lexicographically would also be deterministic, but "jobs.10" sorts
// before "jobs.2", so it would pick an arbitrary element of any list of
// ten or more.
func (i *Index) buildAliases(names map[string]string) {
	if len(names) == 0 {
		return
	}
	paths := make([]string, 0, len(i.byPath))
	for path := range i.byPath {
		paths = append(paths, path)
	}
	slices.SortFunc(paths, func(a, b string) int {
		ea, eb := i.byPath[a], i.byPath[b]
		if c := cmp.Compare(ea.Pos.Line, eb.Pos.Line); c != 0 {
			return c
		}
		if c := cmp.Compare(ea.Pos.Column, eb.Pos.Column); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})

	for _, path := range paths {
		e := i.byPath[path]
		alias, ok := aliasFor(path, names)
		if !ok {
			continue
		}
		e.Alias = alias
		i.byPath[path] = e
		if _, exists := i.byAlias[alias]; !exists {
			i.byAlias[alias] = e
		}
		// Keep byExpr's copies consistent with byPath's.
		for idx, cand := range i.byExpr[e.Expr] {
			if cand.Path == path {
				i.byExpr[e.Expr][idx] = e
			}
		}
	}
}

func aliasFor(path string, names map[string]string) (string, bool) {
	orig := strings.Split(path, ".")
	out := append([]string(nil), orig...)
	changed := false
	for i := range orig {
		if _, err := strconv.Atoi(orig[i]); err != nil {
			continue
		}
		// Prefixes are always built from the ORIGINAL segments: names is
		// keyed by numeric paths, so substituting as we go would make
		// later lookups miss.
		if n, ok := names[strings.Join(orig[:i+1], ".")]; ok && n != "" {
			out[i] = n
			changed = true
		}
	}
	if !changed {
		return "", false
	}
	return strings.Join(out, "."), true
}

// convertPath turns a goccy path such as "$.jobs[0].cmd" into graft's
// dotted form "jobs.0.cmd". It returns false for any path graft's cursor
// syntax cannot represent unambiguously - notably a quoted segment such
// as "$.'a.b'.c", where a key contains a dot. Skipping is correct: a
// wrong path here would attach a confident line number to the wrong
// value.
func convertPath(p string) (string, bool) {
	if !strings.HasPrefix(p, "$") {
		return "", false
	}
	rest := p[1:]
	var segs []string
	for rest != "" {
		switch rest[0] {
		case '.':
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			seg := rest[:end]
			if seg == "" || strings.ContainsAny(seg, "'\"") {
				return "", false
			}
			segs = append(segs, seg)
			rest = rest[end:]
		case '[':
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return "", false
			}
			seg := rest[1:end]
			if _, err := strconv.Atoi(seg); err != nil {
				return "", false
			}
			segs = append(segs, seg)
			rest = rest[end+1:]
		default:
			return "", false
		}
	}
	if len(segs) == 0 {
		return "", false
	}
	return strings.Join(segs, "."), true
}

func splitLast(path string) (base, last string, ok bool) {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return "", "", false
	}
	return path[:i], path[i+1:], true
}

func isNameField(field string) bool {
	return slices.Contains(nameFields, field)
}

func isIndexSegment(path string) bool {
	_, last, ok := splitLast(path)
	if !ok {
		last = path
	}
	_, err := strconv.Atoi(last)
	return err == nil
}

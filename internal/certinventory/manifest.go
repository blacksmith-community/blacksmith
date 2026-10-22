package certinventory

import (
	"crypto/x509"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Literal is a certificate written straight into a manifest value.
type Literal struct {
	// Path is a dotted YAML path with list indices in brackets.
	Path string
	Cert *x509.Certificate
	// Group is the instance group the value sits in, or empty.
	Group string
}

// JobFacts describes one job of an instance group.
type JobFacts struct {
	Group string
	Job   string
	// Properties holds the job's properties minus any private key. It can
	// still hold other secrets such as passwords, so callers must never log
	// it or return it.
	Properties map[string]interface{}
	// CertRefs maps a dotted property path to the variable named in a
	// ((name.certificate)) reference.
	CertRefs map[string]string
}

// ManifestFacts holds what the scanner needs from a deployed manifest.
type ManifestFacts struct {
	Name string
	// Literals holds one entry per certificate block found in a string.
	Literals []Literal
	// SigningCAs maps each certificate variable to its options.ca as written.
	SigningCAs    map[string]string
	CertVariables []string
	// Groups maps each instance group to its instance count.
	Groups map[string]int
	Jobs   []JobFacts
}

const (
	pemCertMarker = "BEGIN CERTIFICATE"
	pemKeyMarker  = "PRIVATE KEY"
)

var certRefPattern = regexp.MustCompile(`\(\(\s*([^()\s]+?)\.certificate\s*\)\)`)

// WalkManifest reads a deployed manifest and collects its certificate facts.
// It never reports a value that holds a private key.
func WalkManifest(manifest string) (*ManifestFacts, error) {
	return walkManifest(manifest, walkLimits{aliases: maxAliasUses, nodes: maxNodesVisited})
}

// walkLimits holds the budgets for one pass of the walk.
type walkLimits struct {
	aliases int
	nodes   int
}

func walkManifest(manifest string, limits walkLimits) (*ManifestFacts, error) {
	var doc yaml.Node

	if err := yaml.Unmarshal([]byte(manifest), &doc); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	if doc.Kind == 0 {
		return nil, errors.New("parse manifest: the manifest is empty")
	}

	w := &walker{
		facts: &ManifestFacts{
			SigningCAs: map[string]string{},
			Groups:     map[string]int{},
		},
		active:  map[*yaml.Node]bool{},
		merging: map[*yaml.Node]bool{},
		limits:  limits,
	}

	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}

	root = w.deref(root)
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("parse manifest: the top level is not a mapping")
	}

	w.top(root)

	if w.err != nil {
		return nil, w.err
	}

	return w.facts, nil
}

// ResolveCredHubName turns a variable or CA name from a manifest into the
// absolute CredHub name. An absolute name is returned unchanged.
func ResolveCredHubName(director, deployment, name string) string {
	if strings.HasPrefix(name, "/") {
		return name
	}

	return "/" + director + "/" + deployment + "/" + name
}

// Limits that keep an untrusted manifest from costing unbounded time or
// memory. The alias and node limits apply to each pass over the manifest,
// because the generic, value, refs, and structural passes each walk the same
// subtrees again. A real director manifest stays far below all of them.
const (
	maxWalkDepth    = 200
	maxAliasUses    = 100000
	maxNodesVisited = 2000000
)

// The passes of the walk. Each one has its own alias and node budget.
const (
	passStructure = iota
	passGeneric
	passValue
	passRefs
	passCount
)

type passBudget struct {
	aliases int
	visited int
}

type walker struct {
	facts   *ManifestFacts
	active  map[*yaml.Node]bool
	merging map[*yaml.Node]bool
	err     error
	depth   int
	limits  walkLimits
	pass    int
	budgets [passCount]passBudget
}

// inPass runs fn with the budgets of the given pass and then returns to the
// pass it was called from. The counters start at zero for each walk.
func (w *walker) inPass(pass int, fn func()) {
	prev := w.pass
	w.pass = pass

	defer func() { w.pass = prev }()

	fn()
}

// stop records the first limit that tripped. Every later step sees the error
// and returns without doing more work.
func (w *walker) stop(format string, args ...interface{}) {
	if w.err == nil {
		w.err = fmt.Errorf("parse manifest: "+format, args...)
	}
}

// enter counts a visit and a level of depth. It reports false when the walk
// must not go further, and leave must follow only after a true result.
func (w *walker) enter() bool {
	if w.err != nil {
		return false
	}

	b := &w.budgets[w.pass]

	b.visited++
	if b.visited > w.limits.nodes {
		w.stop("the manifest is too large to walk (more than %d nodes)", w.limits.nodes)

		return false
	}

	if w.depth >= maxWalkDepth {
		w.stop("the manifest nests deeper than %d levels", maxWalkDepth)

		return false
	}

	w.depth++

	return true
}

func (w *walker) leave() { w.depth-- }

// emptyNode stands in for a node once a limit has tripped, so callers that
// read the result never meet nil.
var emptyNode = &yaml.Node{Kind: yaml.ScalarNode}

// deref follows an alias to the node it names. Each alias followed spends
// from a budget of the current pass.
func (w *walker) deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		b := &w.budgets[w.pass]

		b.aliases++
		if b.aliases > w.limits.aliases {
			w.stop("the manifest uses more than %d aliases", w.limits.aliases)

			return emptyNode
		}

		n = n.Alias
	}

	if w.err != nil {
		return emptyNode
	}

	return n
}

type pair struct{ key, value *yaml.Node }

// pairs returns a mapping's entries with merge keys expanded. Explicit keys
// win over merged ones.
func (w *walker) pairs(n *yaml.Node) []pair {
	if w.err != nil || w.merging[n] {
		return nil
	}

	if !w.enter() {
		return nil
	}
	defer w.leave()

	w.merging[n] = true
	defer delete(w.merging, n)

	var out []pair

	seen := map[string]bool{}

	var merged []pair

	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Tag == "!!merge" {
			src := w.deref(v)
			if src != nil && src.Kind == yaml.MappingNode {
				merged = append(merged, w.pairs(src)...)
			} else if src != nil && src.Kind == yaml.SequenceNode {
				for _, item := range src.Content {
					if m := w.deref(item); m != nil && m.Kind == yaml.MappingNode {
						merged = append(merged, w.pairs(m)...)
					}
				}
			}

			continue
		}

		seen[k.Value] = true

		out = append(out, pair{k, v})
	}

	for _, p := range merged {
		if !seen[p.key.Value] {
			seen[p.key.Value] = true

			out = append(out, p)
		}
	}

	return out
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}

	return base + "." + key
}

func (w *walker) top(root *yaml.Node) {
	for _, p := range w.pairs(root) {
		val := w.deref(p.value)

		switch p.key.Value {
		case "name":
			if val.Kind == yaml.ScalarNode {
				w.facts.Name = val.Value
			}
		case "variables":
			w.variables(val)
			w.inPass(passGeneric, func() { w.generic(val, "variables", "") })
		case "instance_groups":
			w.instanceGroups(val)
		default:
			w.inPass(passGeneric, func() { w.generic(val, p.key.Value, "") })
		}
	}
}

func (w *walker) variables(n *yaml.Node) {
	if n.Kind != yaml.SequenceNode {
		return
	}

	for _, item := range n.Content {
		m := w.deref(item)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}

		var name, typ, ca string

		for _, p := range w.pairs(m) {
			v := w.deref(p.value)

			switch p.key.Value {
			case "name":
				name = scalar(v)
			case "type":
				typ = scalar(v)
			case "options":
				if v.Kind == yaml.MappingNode {
					for _, o := range w.pairs(v) {
						if o.key.Value == "ca" {
							ca = scalar(w.deref(o.value))
						}
					}
				}
			}
		}

		if typ == "certificate" && name != "" {
			w.facts.CertVariables = append(w.facts.CertVariables, name)
			w.facts.SigningCAs[name] = ca
		}
	}
}

func scalar(n *yaml.Node) string {
	if n != nil && n.Kind == yaml.ScalarNode {
		return n.Value
	}

	return ""
}

func (w *walker) instanceGroups(n *yaml.Node) {
	if n.Kind != yaml.SequenceNode {
		return
	}

	for i, item := range n.Content {
		m := w.deref(item)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}

		path := fmt.Sprintf("instance_groups[%d]", i)
		group := ""

		for _, p := range w.pairs(m) {
			if p.key.Value == "name" {
				group = scalar(w.deref(p.value))
			}
		}

		for _, p := range w.pairs(m) {
			v := w.deref(p.value)

			switch p.key.Value {
			case "instances":
				if group != "" && v.Kind == yaml.ScalarNode {
					var count int
					if err := v.Decode(&count); err == nil {
						w.facts.Groups[group] = count
					}
				}

				w.inPass(passGeneric, func() { w.generic(v, joinPath(path, "instances"), group) })
			case "jobs":
				w.jobs(v, joinPath(path, "jobs"), group)
			default:
				w.inPass(passGeneric, func() { w.generic(v, joinPath(path, p.key.Value), group) })
			}
		}
	}
}

func (w *walker) jobs(n *yaml.Node, path, group string) {
	if n.Kind != yaml.SequenceNode {
		return
	}

	for i, item := range n.Content {
		m := w.deref(item)
		if m == nil || m.Kind != yaml.MappingNode {
			continue
		}

		jobPath := fmt.Sprintf("%s[%d]", path, i)
		job := JobFacts{Group: group, CertRefs: map[string]string{}}

		for _, p := range w.pairs(m) {
			v := w.deref(p.value)

			switch p.key.Value {
			case "name":
				job.Job = scalar(v)
			case "properties":
				if v.Kind == yaml.MappingNode {
					w.inPass(passValue, func() {
						props, _ := w.value(v).(map[string]interface{})
						job.Properties = props
					})

					if w.err != nil {
						return
					}

					w.inPass(passRefs, func() { w.refs(v, "", job.CertRefs) })
				}
			}

			w.inPass(passGeneric, func() { w.generic(v, joinPath(jobPath, p.key.Value), group) })
		}

		w.facts.Jobs = append(w.facts.Jobs, job)
	}
}

// generic walks any node looking for certificate strings.
func (w *walker) generic(n *yaml.Node, path, group string) {
	n = w.deref(n)
	if n == nil || w.active[n] {
		return
	}

	if !w.enter() {
		return
	}
	defer w.leave()

	switch n.Kind {
	case yaml.ScalarNode:
		w.literal(n.Value, path, group)
	case yaml.MappingNode:
		w.active[n] = true

		for _, p := range w.pairs(n) {
			w.generic(p.value, joinPath(path, p.key.Value), group)
		}

		delete(w.active, n)
	case yaml.SequenceNode:
		w.active[n] = true

		for i, item := range n.Content {
			w.generic(item, fmt.Sprintf("%s[%d]", path, i), group)
		}

		delete(w.active, n)
	}
}

func (w *walker) literal(text, path, group string) {
	if !strings.Contains(text, pemCertMarker) || strings.Contains(text, pemKeyMarker) {
		return
	}

	certs, err := ParsePEMCertificates([]byte(text))
	if err != nil {
		return
	}

	for _, c := range certs {
		w.facts.Literals = append(w.facts.Literals, Literal{Path: path, Cert: c, Group: group})
	}
}

// value converts a node to plain Go values. It follows aliases, guards
// against cycles, and drops any string that holds a private key.
func (w *walker) value(n *yaml.Node) interface{} {
	n = w.deref(n)
	if n == nil || w.active[n] {
		return nil
	}

	if !w.enter() {
		return nil
	}
	defer w.leave()

	switch n.Kind {
	case yaml.MappingNode:
		w.active[n] = true
		defer delete(w.active, n)

		out := map[string]interface{}{}

		for _, p := range w.pairs(n) {
			if s := w.deref(p.value); s != nil && s.Kind == yaml.ScalarNode && strings.Contains(s.Value, pemKeyMarker) {
				continue
			}

			out[p.key.Value] = w.value(p.value)
		}

		return out
	case yaml.SequenceNode:
		w.active[n] = true
		defer delete(w.active, n)

		out := make([]interface{}, 0, len(n.Content))

		for _, item := range n.Content {
			if s := w.deref(item); s != nil && s.Kind == yaml.ScalarNode && strings.Contains(s.Value, pemKeyMarker) {
				continue
			}

			out = append(out, w.value(item))
		}

		return out
	case yaml.ScalarNode:
		var v interface{}
		if err := n.Decode(&v); err != nil {
			return n.Value
		}

		return v
	}

	return nil
}

// refs records every ((name.certificate)) reference under a properties
// node, keyed by the dotted property path.
func (w *walker) refs(n *yaml.Node, path string, out map[string]string) {
	n = w.deref(n)
	if n == nil || w.active[n] {
		return
	}

	if !w.enter() {
		return
	}
	defer w.leave()

	switch n.Kind {
	case yaml.ScalarNode:
		if strings.Contains(n.Value, pemKeyMarker) {
			return
		}

		if m := certRefPattern.FindStringSubmatch(n.Value); m != nil {
			out[path] = m[1]
		}
	case yaml.MappingNode:
		w.active[n] = true

		for _, p := range w.pairs(n) {
			w.refs(p.value, joinPath(path, p.key.Value), out)
		}

		delete(w.active, n)
	case yaml.SequenceNode:
		w.active[n] = true

		for i, item := range n.Content {
			w.refs(item, fmt.Sprintf("%s[%d]", path, i), out)
		}

		delete(w.active, n)
	}
}

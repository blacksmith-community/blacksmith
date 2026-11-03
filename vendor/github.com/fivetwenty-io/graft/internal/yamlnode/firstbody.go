package yamlnode

import "github.com/goccy/go-yaml/ast"

// FirstBody returns the root node of file's first document, or nil when
// it has none. goccy gives the directives before a "---" a document of
// their own, whose body is the directive, so FirstBody skips those and
// returns the body of the document the directives belong to, as libyaml
// reads it. It skips nothing else. yaml.Unmarshal also skips an empty or
// null document, but the merge reads that document as {}, as spruce
// does, and never the document after it.
func FirstBody(file *ast.File) ast.Node {
	if file == nil {
		return nil
	}
	for _, doc := range file.Docs {
		if doc == nil {
			return nil
		}
		if _, ok := doc.Body.(*ast.DirectiveNode); !ok {
			return doc.Body
		}
	}
	return nil
}

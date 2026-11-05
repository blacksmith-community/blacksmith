// Package exprlex holds the tokenizer options that graft's expression
// parser lexes with. The parser in pkg/graft and the loop-body rewrite in
// pkg/graft/controlflow both take their options from here, so the rewrite
// reads an expression with the same rules the parser does and the two
// cannot drift apart.
package exprlex

import "github.com/fivetwenty-io/graft/pkg/graft/interfaces"

// Options returns the tokenizer options the expression parser uses.
// operatorName reports whether a name is a registered operator, which the
// tokenizer needs to decide whether a '/' right after that name begins a
// reference, as in "(( grab /b ))".
func Options(operatorName func(name string) bool) interfaces.TokenizerOptions {
	return interfaces.TokenizerOptions{
		RecognizeReferencePaths: true,
		AllowEnvironmentVars:    true,
		TrackPositions:          true,
		AllowUnicode:            true,
		OperatorName:            operatorName,
	}
}

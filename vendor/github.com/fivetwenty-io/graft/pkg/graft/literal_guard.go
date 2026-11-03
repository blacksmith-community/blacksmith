package graft

import (
	"errors"
	"strings"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// guardLiteralResponse keeps text that an operator derived from a literal
// from being run as an operator later. Most operators read their
// arguments as plain data, so a (( join )) or a || fallback over a
// literal returns plain strings, and a "(( ... ))" string among them
// would be evaluated by a later phase or a later Evaluate. When op read
// from a literal, every such string in its result is wrapped again.
//
// (( defer )) is left alone: it writes the text of its own arguments,
// which come from the template, not the value they point at.
func (ev *Evaluator) guardLiteralResponse(op *Opcall, resp *Response) {
	if !ev.holdsLiteral || resp == nil || op == nil || op.name == "defer" {
		return
	}
	if resp.Type != Replace && resp.Type != Inject {
		return
	}
	if !ev.argsReadLiteral(op.args) {
		return
	}
	if guarded, changed := literal.GuardText(resp.Value); changed {
		resp.Value = guarded
	}
}

// argsReadLiteral reports whether any of args can read a value from
// inside a literal.
func (ev *Evaluator) argsReadLiteral(args []*Expr) bool {
	for _, arg := range args {
		if ev.exprReadsLiteral(arg) {
			return true
		}
	}
	return false
}

// exprReadsLiteral reports whether e can read a value from inside a
// literal. A reference counts when it resolves to a literal, into one,
// or to a value that holds one. A string that reads as such a path
// counts too, since some operators take a path as a string. A nested
// call counts when its own arguments do, and also when any of them is
// computed, because the call may then use that result as a path.
func (ev *Evaluator) exprReadsLiteral(e *Expr) bool {
	if e == nil {
		return false
	}
	switch e.Type {
	case Reference:
		if e.Reference == nil {
			return false
		}
		if strings.Contains(e.Reference.String(), "$") {
			// The path holds an environment variable that is expanded
			// when the operator runs, so it can point anywhere.
			return true
		}
		return ev.pathReadsLiteral(e.Reference)
	case Literal:
		s, ok := e.Literal.(string)
		if !ok {
			return false
		}
		c, err := tree.ParseCursor(s)
		if err != nil {
			return false
		}
		return ev.pathReadsLiteral(c)
	case OperatorCall:
		for _, arg := range e.Args() {
			if arg == nil {
				continue
			}
			if arg.Type != Literal && arg.Type != Reference {
				return true
			}
			if ev.exprReadsLiteral(arg) {
				return true
			}
		}
		return false
	default:
		return ev.exprReadsLiteral(e.Left) || ev.exprReadsLiteral(e.Right)
	}
}

// pathReadsLiteral reports whether c resolves to a literal, into one, or
// to a value that holds one.
func (ev *Evaluator) pathReadsLiteral(c *tree.Cursor) bool {
	v, err := c.ResolveLiteral(ev.Tree)
	if err != nil {
		return false
	}
	return literal.Contains(v)
}

// minWithheldText is the shortest literal text that withholds an error
// message. A shorter one, such as "a" or "80", turns up in too many
// ordinary messages to tell anything about the literal.
const minWithheldText = 3

// withheldError stands in for an error whose Error() text quotes text
// from a literal. Operator messages quote no values, so the text gets
// there only through a path the operator names, such as one built from
// the literal's content. The original stays off the Unwrap chain, since
// its text is what is withheld. errors.Is still sees every error in it,
// errors.As sees the ones whose text quotes none of the literal texts,
// and the detailed text is the original's.
type withheldError struct {
	cause error
	texts []string
}

func (e *withheldError) Error() string {
	return "the operator failed on a value from a literal; the message is withheld because it quotes that value"
}

// DetailedError returns the withheld error's detailed text.
func (e *withheldError) DetailedError() string {
	return errtext.Detail(e.cause)
}

func (e *withheldError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func (e *withheldError) As(target any) bool {
	return errtext.AsHiddenWhere(e.cause, target, func(err error) bool {
		return errtext.Exposable(err) && quotedLiteral(err.Error(), e.texts) == ""
	})
}

// quotedLiteral returns the first of texts that msg holds, or "" when it
// holds none. Texts shorter than minWithheldText are skipped, since they
// match too much ordinary message text.
func quotedLiteral(msg string, texts []string) string {
	for _, text := range texts {
		if len(text) >= minWithheldText && strings.Contains(msg, text) {
			return text
		}
	}
	return ""
}

// scrubLiteralError returns err, or a withheldError in its place when op
// read from a literal and the error's Error() text holds text from a
// literal in the tree. An operator that read no literal keeps its
// message, even when that message happens to hold the same text as a
// literal elsewhere. The path of a PathError is kept, so the message
// still says where the error is.
func (ev *Evaluator) scrubLiteralError(op *Opcall, err error) error {
	if err == nil || !ev.holdsLiteral {
		return err
	}
	if op == nil || !ev.argsReadLiteral(op.args) {
		return err
	}
	texts := literal.Texts(ev.Tree)
	if quotedLiteral(err.Error(), texts) == "" {
		return err
	}
	if pathErr, ok := err.(*PathError); ok { //nolint:errorlint // only the outer PathError's path is kept
		return &PathError{Path: pathErr.Path, Cause: &withheldError{cause: pathErr.Cause, texts: texts}}
	}
	return &withheldError{cause: err, texts: texts}
}

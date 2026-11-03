package graft

import "github.com/fivetwenty-io/graft/internal/errtext"

// DetailedError returns the full text of err, as the graft CLI prints it.
//
// The Error() text of every error graft returns is plain. It holds no
// ANSI escape codes and quotes no value from a document, so a library
// caller can log it or show it in an API without leaking a credential.
// It may name document paths, operator names, YAML tags, Go types, and
// line and column numbers. A path that the document builds from values,
// such as the path in a grab of a concat, names those values, so the
// Error() text of an error at that path can carry them. When an
// operator read from a literal and its message holds a literal key or
// scalar of 3 or more characters, graft withholds that message instead.
// A shorter literal text can still appear in Error(), and DetailedError
// always quotes the literal text the original message held.
//
// The same holds for every error a caller can reach from the returned
// one through errors.Unwrap or errors.As, so DetailedError is the only
// way to the full text. It adds back what Error() leaves out, which is
// the values an error quoted, the source text of the operator that
// failed, and, when the CLI has turned color on, the escape codes.
// Callers that relied on values or color in Error() before 1.45.0 call
// DetailedError instead, and only where its text is safe to show.
//
// err may be any error. DetailedError walks wrappers built by
// fmt.Errorf and errors.Join, so an error wrapped by the caller keeps
// its detail. It returns err.Error() for an error that carries nothing
// more, and "" for a nil err.
func DetailedError(err error) string {
	return errtext.Detail(err)
}

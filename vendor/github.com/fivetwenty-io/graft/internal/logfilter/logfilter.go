// Package logfilter holds the hook that the log package consults for
// each DEBUG and TRACE message. pkg/graft sets it so that traces never
// name a loop's internal bindings.
package logfilter

// Hook sees each DEBUG and TRACE message when it is non-nil, all of its
// lines together, before it is written. It returns the message to write
// in its place, and false to leave the whole message out.
var Hook func(message string) (string, bool)

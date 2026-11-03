package yaml

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
)

// decoderModule is the module whose panics a decode turns into an
// error. goccy panics on some target shapes, such as an unexported
// embedded struct whose key the document holds, either itself or
// through package reflect.
const decoderModule = "github.com/goccy/go-yaml"

// inDecoder reports whether function, a frame's function name, is in
// decoderModule or in package reflect. A name has the module's path, or
// "reflect", followed by a dot or a slash, so a look-alike path such as
// github.com/goccy/go-yamlx does not match, and only the standard
// library's reflect has a name with no slash. panicStartedIn passes over
// reflect's frames before it calls inDecoder, so the reflect check is a
// fallback that keeps inDecoder correct on its own.
func inDecoder(function string) bool {
	if rest, ok := strings.CutPrefix(function, decoderModule); ok {
		return strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "/")
	}
	return isReflect(function)
}

// panicFromDecoder reports whether the panic being recovered started in
// goccy or in package reflect, rather than in a caller's own method that the
// decoder ran. It must be called from the deferred function that
// recovers, while the panicking frames are still on the stack. It finds
// the first frame below runtime.gopanic that is in neither the runtime
// nor package reflect, which is the code that caused the panic.
func panicFromDecoder() bool {
	return panicStartedIn(inDecoder)
}

// encoderModule is graft's own encoder, whose panics an encode turns
// into an error along with goccy's and reflect's.
const encoderModule = "github.com/fivetwenty-io/graft/internal/yamlcodec"

// walkModule is this package, whose walk over a caller's value reads it
// with package reflect.
const walkModule = "github.com/fivetwenty-io/graft/pkg/yaml"

// inEncoder reports whether function is in the packages an encode runs
// on a caller's value, which are inDecoder's and graft's own encoder.
func inEncoder(function string) bool {
	for _, module := range []string{encoderModule, walkModule} {
		if rest, ok := strings.CutPrefix(function, module); ok && strings.HasPrefix(rest, ".") {
			return true
		}
	}
	return inDecoder(function)
}

// panicFromEncoder is panicFromDecoder for an encode, and the same
// rules for where it is called apply.
func panicFromEncoder() bool {
	return panicStartedIn(inEncoder)
}

// isReflect reports whether function is in the standard library's
// package reflect.
func isReflect(function string) bool {
	return strings.HasPrefix(function, "reflect.") && !strings.Contains(function, "/")
}

// panicStartedIn reports whether the panic being recovered started in a
// frame that in accepts. Frames in the runtime and in package reflect
// are passed over, because reflect raises its panics for whoever called
// it, and the frame that called it decides. So a panic that reflect
// raises for a caller's own method, which lies between reflect and the
// decoder, belongs to that method, and one that reflect raises for the
// decoder itself belongs to the decoder.
func panicStartedIn(in func(function string) bool) bool {
	pcs := make([]uintptr, 128)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs)])
	panicking, sawReflect := false, false
	for {
		frame, more := frames.Next()
		switch {
		case frame.Function == "runtime.gopanic":
			panicking = true
		case panicking && isReflect(frame.Function):
			sawReflect = true
		case panicking && !strings.HasPrefix(frame.Function, "runtime."):
			return in(frame.Function)
		}
		if !more {
			// The stack ended inside reflect, so no caller's frame was
			// found and the decoder is the likelier source.
			return sawReflect
		}
	}
}

// decoderPanicError is the error a decode returns in place of a decoder
// panic. It names only the target's Go type, since a panic's value can
// quote the input, and it has no position, since a panic carries none.
func decoderPanicError(t reflect.Type) *DecodeError {
	return &DecodeError{Kind: ErrorKindType, Msg: fmt.Sprintf("cannot decode into Go value of type %s", t)}
}

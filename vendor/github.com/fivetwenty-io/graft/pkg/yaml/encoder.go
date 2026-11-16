package yaml

import (
	"errors"
	"io"
)

// Encoder writes a stream of YAML documents to an io.Writer. Each call
// to Encode produces the same bytes as MarshalWith for that value, and
// documents after the first are preceded by a "---" separator line. An
// Encoder is not safe for concurrent use.
//
// When the writer fails, Encode returns its error, and whatever part of
// the document the writer took stays in the stream. A later Encode still
// writes its document right after that partial text, with a separator
// line only when an earlier document was written in full, so the stream
// no longer reads as the documents we encoded. As with encoding/json, we
// should stop using an Encoder after a write fails.
type Encoder struct {
	w      io.Writer
	opts   EncodeOptions
	wrote  bool
	closed bool
}

// NewEncoder returns an Encoder that writes to w with the default options,
// matching Marshal.
func NewEncoder(w io.Writer) *Encoder {
	return NewEncoderWith(w, EncodeOptions{})
}

// NewEncoderWith returns an Encoder that writes to w with the options
// given, matching MarshalWith.
func NewEncoderWith(w io.Writer, opts EncodeOptions) *Encoder {
	return &Encoder{w: w, opts: opts}
}

// Encode writes v as the next document in the stream. A value that fails
// to marshal writes nothing and does not count as a document. An error
// from the underlying writer is returned as it is.
func (e *Encoder) Encode(v any) error {
	if e.closed {
		return errors.New("yaml: Encode called after Close")
	}
	doc, err := marshalWith(v, e.opts, e.wrote)
	if err != nil {
		return err
	}
	if _, err := e.w.Write(doc); err != nil {
		return err
	}
	e.wrote = true
	return nil
}

// Close ends the stream. It writes nothing, may be called more than once,
// and makes every later Encode fail.
func (e *Encoder) Close() error {
	e.closed = true
	return nil
}

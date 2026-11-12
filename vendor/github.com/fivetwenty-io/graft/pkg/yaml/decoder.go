package yaml

import (
	"io"

	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// Decoder reads a stream of YAML documents from an io.Reader, one
// document for each call to Decode. Each document decodes into its target
// as UnmarshalWith decodes it, with the same routes, rules, and errors.
// The one difference is that lines count from the start of the stream
// rather than the start of the document, whether they appear in an error,
// in a duplicate key report, or in a Node.
//
// On its first Decode, a Decoder reads all of r before it decodes
// anything, as goccy/go-yaml's decoder does, and then cuts the stream
// into documents the way yaml.v3 counts them. A document that holds only
// comments, directives, or markers, such as the empty document after a
// final "---", is skipped, wherever it sits in the stream. goccy skips
// such a document only at the end, and fails when one sits between two
// documents. Content that follows a "..." with no "---" before it fails,
// just as it does in Unmarshal, although goccy accepts it.
//
// Once the documents run out, Decode returns io.EOF. Only an error in the
// framing of the stream ends it, and every later Decode returns that same
// error. Those errors are an error from r, input that is not valid UTF-16
// after a UTF-16 byte order mark, and content that follows a "..." with
// no "---" before it. Any error inside a document, whether a syntax
// error, a limit error, a type error, or an error an UnmarshalYAML method
// returns, ends only that document, so the next Decode moves on to the
// next one. Each document decodes on its own, so an alias cannot refer to
// an anchor that an earlier document defines.
//
// A Decoder is not safe for concurrent use.
type Decoder struct {
	r    io.Reader
	opts DecodeOptions
	read bool
	docs []yamlnode.StreamDocument
	next int
	err  error
}

// NewDecoder returns a Decoder that reads from r with the default
// options, matching Unmarshal.
func NewDecoder(r io.Reader) *Decoder {
	return NewDecoderWith(r, DecodeOptions{})
}

// NewDecoderWith returns a Decoder that reads from r with opts, matching
// UnmarshalWith.
func NewDecoderWith(r io.Reader, opts DecodeOptions) *Decoder {
	return &Decoder{r: r, opts: opts}
}

// Decode decodes the next document of the stream into v, which must be a
// non-nil pointer, as for Unmarshal. Decode refuses some targets with an
// ErrorKindType error, and that error consumes no document. Decode returns
// io.EOF when no document is left.
func (d *Decoder) Decode(v any) error {
	if err := checkTargetFor("Decode", v); err != nil {
		return err
	}
	if !d.read {
		d.read = true
		d.err = d.split()
	}
	for d.err == nil && d.next < len(d.docs) {
		doc := d.docs[d.next]
		d.next++
		if doc.Err != nil {
			d.err = translate(doc.Err)
			break
		}
		if doc.Empty {
			continue
		}
		return unmarshalAt([]byte(doc.Text), v, d.opts, doc.LineOffset)
	}
	if d.err != nil {
		return d.err
	}
	return io.EOF
}

// split reads all of r and cuts it into the documents Decode walks. Its
// error is the reader's own, or the error for input that is not valid
// UTF-16 after a UTF-16 byte order mark.
func (d *Decoder) split() error {
	data, err := io.ReadAll(d.r)
	if err != nil {
		return err
	}
	data, err = toUTF8(data)
	if err != nil {
		return err
	}
	d.docs = yamlnode.SplitStream(string(data))
	return nil
}

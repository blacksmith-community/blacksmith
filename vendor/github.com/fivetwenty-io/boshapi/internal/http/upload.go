package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// ErrStreamBodyInvalid is returned, before anything is sent, for a streamed
// body that has no reader or no positive size.
var ErrStreamBodyInvalid = errors.New("streamed body is not valid")

// PostStream performs a POST whose body is read from body as it is sent, for
// an upload that may be gigabytes long. The body is never held in memory, and
// it goes out with the given content type and a Content-Length of size, as
// bosh-cli sends a local tarball. Exactly size bytes are read from body and
// sent, and anything after them is left unread. A body that ends before size
// bytes fails the request before the director has all of it. The caller keeps
// ownership of body and closes it. PostStream never closes it.
//
// The request goes through the stream client, so HTTPTimeout bounds only the
// wait for the response headers after the body is written, and ctx bounds
// the whole upload. A reader cannot be replayed, so the request is sent at
// most once: it is never retried, whatever failed, and a 401 is never
// resent. A 401 to a request that carried a bearer token still replaces the
// token the director rejected, so the caller's next upload carries a new one.
// The failure is returned for the caller to act on. A zero or negative size, or a nil
// body, returns an error wrapping ErrStreamBodyInvalid.
//
// PostStream reads body on a goroutine of its own, so the end of ctx ends the
// call even while a Read on body is blocked. When PostStream returns, nothing
// reads body any more, with one exception. If ctx ended while a Read on body
// was blocked, that one Read may still be running, and no further Read is
// made once it returns. Closing body makes most readers, such as an *os.File
// or an *io.PipeReader, return from a blocked Read, but a reader that ignores
// Close keeps that goroutine parked in Read until it returns, and the caller
// must not reuse such a reader before then. When the request ends before the
// whole body is sent, because the director answers early, as its nginx does
// with a 413, or because the connection drops with no answer, PostStream
// stops sending and waits for a Read in progress to return before it
// returns. A reader that blocks at that moment holds the call until the Read
// returns or ctx ends, so give ctx a deadline when the reader can stall.
func (c *Client) PostStream(
	ctx context.Context, path string, query url.Values, contentType string, body io.Reader, size int64,
) (*Response, error) {
	if body == nil || size <= 0 {
		return nil, fmt.Errorf("%w: POST %s needs a reader and a size above zero, and got size %d; nothing was sent",
			ErrStreamBodyInvalid, path, size)
	}

	return c.Do(ctx, &Request{
		Method:      http.MethodPost,
		Path:        path,
		Query:       query,
		ContentType: contentType,
		Stream:      body,
		StreamSize:  size,
	})
}

// errStreamBodyClosed and errStreamFinished end the pipe of a uploadBody.
// Only the goroutine that copies the caller's reader into the pipe sees
// them, after the transport has stopped reading.
var (
	errStreamBodyClosed = errors.New("the transport closed the streamed body")
	errStreamFinished   = errors.New("the streamed request was answered or abandoned")
)

// uploadBody is the body the transport reads for a streamed request. The
// transport never reads the caller's reader itself, because net/http cannot
// end a Read that blocks, and it waits for its write loop before it returns
// from a cancelled request. A goroutine of the body's own copies the caller's
// reader into a pipe, and the transport reads the other end, so the pipe can
// be closed under the transport at any time. That goroutine starts on the
// first Read, so a request that never writes its body leaves the reader
// untouched.
type uploadBody struct {
	source io.Reader
	// size is the Content-Length. Exactly size bytes are read from source.
	size  int64
	pipeR *io.PipeReader
	pipeW *io.PipeWriter
	// start runs exactly once, either to start the copy on the first Read
	// or, in finish, to mark a copy that never started as done.
	start sync.Once
	// done is closed once nothing reads source any more.
	done chan struct{}
	// stopAfterFunc unhooks the close that the end of the context triggers.
	stopAfterFunc func() bool
}

// newUploadBody returns the body for a stream of size bytes read from
// source. When ctx ends, the pipe closes with the context's error, so a
// transport Read that waits on a blocked source returns at once.
func newUploadBody(ctx context.Context, source io.Reader, size int64) *uploadBody {
	pipeR, pipeW := io.Pipe()

	body := &uploadBody{source: source, size: size, pipeR: pipeR, pipeW: pipeW, done: make(chan struct{})}
	body.stopAfterFunc = context.AfterFunc(ctx, func() {
		_ = pipeW.CloseWithError(ctx.Err())
	})

	return body
}

// Read hands the transport the next bytes of the caller's reader.
func (b *uploadBody) Read(p []byte) (int, error) {
	b.start.Do(func() { go b.copySource() })

	return b.pipeR.Read(p) //nolint:wrapcheck // the transport reports the reader's own error
}

// Close is called by the transport once it stops reading the body. It ends
// the copy at its next write to the pipe, and it never waits for the copy,
// because the transport calls it from the write loop that a cancelled request
// waits for.
func (b *uploadBody) Close() error {
	_ = b.pipeW.CloseWithError(errStreamBodyClosed)

	return nil
}

// bodyCutShortError marks an error that stopped a streamed body before size
// bytes were read from the caller's reader, because that reader ended early
// or failed. It unwraps to the cause, and newTransportError reads it to tell
// the caller that the director cannot have accepted the request.
type bodyCutShortError struct {
	err error
}

func (e *bodyCutShortError) Error() string {
	return e.err.Error()
}

func (e *bodyCutShortError) Unwrap() error {
	return e.err
}

// failureReader marks any error other than io.EOF that its reader returns as
// a bodyCutShortError, so it can be told from an error of the pipe the bytes
// are copied into.
type failureReader struct {
	reader io.Reader
}

func (r *failureReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &bodyCutShortError{err: fmt.Errorf("reading the upload body: %w", err)}
	}

	return n, err //nolint:wrapcheck // io.EOF must reach io.Copy unwrapped
}

// copySource copies size bytes of the caller's reader into the pipe and
// passes the reader's error on to the transport. It never reads past size, so
// a longer reader sends exactly size bytes and the rest stays unread. A reader
// that ends before size fails the request before the body is complete, so the
// director never gets a whole request.
func (b *uploadBody) copySource() {
	defer close(b.done)

	source := &failureReader{reader: io.LimitReader(b.source, b.size)}

	copied, err := io.Copy(b.pipeW, source)
	if err == nil && copied < b.size {
		err = &bodyCutShortError{err: fmt.Errorf("the reader ended after %d of the %d bytes its size gave, "+
			"so the body was cut short and the request failed before the director got all of it: %w",
			copied, b.size, io.ErrUnexpectedEOF)}
	}

	_ = b.pipeW.CloseWithError(err)
}

// finish ends the stream once the transport has returned, whether with an
// answer or an error. It closes the pipe, so the copy stops at its next
// write, and it returns once nothing reads the caller's reader any more, or
// once ctx ends while a Read on that reader is blocked. A nil body does
// nothing.
func (b *uploadBody) finish(ctx context.Context) {
	if b == nil {
		return
	}

	b.stopAfterFunc()
	_ = b.pipeW.CloseWithError(errStreamFinished)
	b.start.Do(func() { close(b.done) })

	select {
	case <-b.done:
	case <-ctx.Done():
	}
}

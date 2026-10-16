package http

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Request represents an HTTP request.
type Request struct {
	Method string
	Path   string
	// Query holds the query parameters. A key with several values is sent
	// once for each value.
	Query   url.Values
	Body    interface{}
	RawBody []byte
	// Stream is a body that is read as it is sent, and StreamSize is its
	// length in bytes. A stream cannot be replayed, so the request is sent
	// at most once: it is never retried and a 401 does not resend it,
	// though a 401 still replaces the rejected bearer token. Do
	// reads it on a goroutine of its own and returns once nothing reads it
	// any more, or once the context ends, as PostStream describes.
	Stream      io.Reader
	StreamSize  int64
	ContentType string
	Headers     map[string]string
	// CreatesTask marks a GET that makes the director start a task. It is
	// treated like a mutation and never resent blindly.
	CreatesTask bool
	// NoTaskRedirect marks a mutation that does not start a task, such as
	// the cancel of a task. Any redirect to it is a *boshapi.RedirectError,
	// even a 302 or 303 to /tasks/<id>, because only a 2xx answer confirms that the
	// director accepted it.
	NoTaskRedirect bool
	// NoAuth sends the request without credentials, such as the GET /info
	// that reads how the director authenticates.
	NoAuth bool
	// KeepsDiffError marks a diff request. The director answers one it
	// cannot compute with {"diff":[],"error":"..."}, whose text can run long
	// and which the caller reads, so an APIError keeps up to
	// maxDiffErrorDescription bytes of that body in place of the shorter cut
	// every other route gets. Only the diff calls set it, because a path
	// that merely ends in "/diff" may be a name the caller supplied.
	KeepsDiffError bool
	isRetry        bool
}

// Response represents an HTTP response.
type Response struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
	// Task is the task a 302 or 303 answer pointed at, read from the Location
	// header. It is nil for any other answer.
	Task *boshapi.Task

	// method, path, and baseURL name the request and the director it went
	// to, for the error RequireTask returns.
	method  string
	path    string
	baseURL string
	// contextID is the context ID the request carried, or empty.
	contextID string
	// sent is the credentials the request carried, which no error or log
	// line made from the body may repeat.
	sent sentAuth
}

// maxTaskAnswerBody is the most of an unexpected answer's body that a
// TaskAnswerError keeps.
const maxTaskAnswerBody = 1024

// RequireTask returns the task a request that starts a director task
// created. The director answers every such request with a 302 or 303 to /tasks/<id>,
// and the client turns that into Task without asking the director anything
// more. Any other answer returns a *boshapi.TaskAnswerError, because the
// director may have started a task whose ID the client cannot read.
func (r *Response) RequireTask() (*boshapi.Task, error) {
	if r.Task != nil {
		return r.Task, nil
	}

	// The credentials are hidden before the cut, so one that the cut would
	// have split is still matched in full.
	body := r.sent.hide(strings.TrimSpace(string(r.Body)))
	if len(body) > maxTaskAnswerBody {
		body = strings.ToValidUTF8(body[:maxTaskAnswerBody], "")
	}

	return nil, &boshapi.TaskAnswerError{
		Method:      r.method,
		Path:        r.path,
		Status:      r.StatusCode,
		Body:        body,
		DirectorURL: r.baseURL,
		ContextID:   r.contextID,
	}
}

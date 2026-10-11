// Package boshtest provides an in-process BOSH director for tests. It serves
// TLS, so a test hands the adapter its CA certificate exactly as production
// does, and it answers GET /info the way a director that uses HTTP Basic
// authentication does, so the adapter's auth discovery succeeds without a
// network. The package imports nothing from Blacksmith, so tests inside
// package bosh can use it too.
package boshtest

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Director error codes the real director uses in its JSON error bodies.
const (
	CodeDirectorNotFound   = 20000
	CodeTaskNotFound       = 10000
	CodeDeploymentNotFound = 70000
)

// Request is one request the test director received.
type Request struct {
	Method   string
	Path     string
	RawQuery string
}

// Handler answers one request. It returns true when it wrote an answer, and
// false to leave the request to the director's defaults.
type Handler func(writer http.ResponseWriter, request *http.Request) bool

// Director is a TLS test server that behaves like a BOSH director.
type Director struct {
	// Server is the underlying test server.
	Server *httptest.Server
	// URL is the address to give the adapter as the director address.
	URL string
	// CACert is the PEM text of the certificate the server presents, to give
	// the adapter as the director CA.
	CACert string

	mu       sync.Mutex
	requests []Request
}

// New starts a test director and closes it when the test ends. The handler
// may be nil. Every request is recorded before the handler sees it. A
// request the handler declines gets the director's defaults, which are an
// HTTP Basic answer for /info, and the director's own plain 404 for any
// other path.
func New(t testing.TB, handler Handler) *Director {
	t.Helper()

	director := &Director{}

	director.Server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		director.record(request)

		if handler != nil && handler(writer, request) {
			return
		}

		if request.URL.Path == "/info" {
			WriteJSON(writer, http.StatusOK, map[string]interface{}{
				"name":    "test-director",
				"uuid":    "00000000-0000-0000-0000-000000000000",
				"version": "282.0.0 (00000000)",
				"user":    nil,
				"cpi":     "test_cpi",
				"user_authentication": map[string]interface{}{
					"type":    "basic",
					"options": map[string]interface{}{},
				},
			})

			return
		}

		WriteRouteNotFound(writer, request)
	}))
	t.Cleanup(director.Server.Close)

	director.URL = director.Server.URL
	director.CACert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: director.Server.Certificate().Raw}))

	return director
}

func (d *Director) record(request *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.requests = append(d.requests, Request{
		Method:   request.Method,
		Path:     request.URL.Path,
		RawQuery: request.URL.RawQuery,
	})
}

// Requests returns every request received so far, oldest first, including the
// GET /info that auth discovery sends.
func (d *Director) Requests() []Request {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]Request(nil), d.requests...)
}

// APIRequests returns the requests received so far other than GET /info, which
// is the traffic a test asserts on.
func (d *Director) APIRequests() []Request {
	var out []Request

	for _, request := range d.Requests() {
		if request.Method == http.MethodGet && request.Path == "/info" {
			continue
		}

		out = append(out, request)
	}

	return out
}

// WriteJSON answers with a JSON body.
func WriteJSON(writer http.ResponseWriter, status int, body interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)

	_ = json.NewEncoder(writer).Encode(body)
}

// WriteError answers the way the director answers a failure, with its JSON
// error body.
func WriteError(writer http.ResponseWriter, status, code int, description string) {
	WriteJSON(writer, status, map[string]interface{}{"code": code, "description": description})
}

// WriteText answers with a plain text body, the way the director answers some
// failures, such as a cancel of a task that is no longer running.
func WriteText(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "text/plain")
	writer.WriteHeader(status)

	_, _ = writer.Write([]byte(body))
}

// DropAnswer closes the connection without writing an answer, the way a
// connection that breaks after the request reached the director does. The
// client sees its request sent and no answer come back.
func DropAnswer(writer http.ResponseWriter) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		panic("boshtest: the response writer cannot drop the connection")
	}

	conn, _, err := hijacker.Hijack()
	if err != nil {
		panic("boshtest: dropping the connection failed: " + err.Error())
	}

	_ = conn.Close()
}

// WriteRouteNotFound answers the way the director answers a route it does not
// serve, with a 404 and a plain text body that holds no error code.
func WriteRouteNotFound(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "text/plain")
	writer.WriteHeader(http.StatusNotFound)

	_, _ = writer.Write([]byte("Endpoint '" + request.URL.Path + "' not found."))
}

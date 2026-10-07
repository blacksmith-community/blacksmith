package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"
)

// contextIDHeader is the header a director request carries its context ID
// in.
const contextIDHeader = "X-Bosh-Context-Id"

// contextIDParameter is the query parameter a task lookup by context ID
// carries the ID in.
const contextIDParameter = "context_id"

// certificateLifetime is how long the certificate the proxy serves stays
// valid.
const certificateLifetime = 7 * 24 * time.Hour

// serialNumberBits is the size of the certificate's random serial number.
const serialNumberBits = 128

var (
	// errAnswerDropped marks the director answer the proxy drops.
	errAnswerDropped = errors.New("the director's answer was dropped")

	// errNoDirector is a configuration that names no director.
	errNoDirector = errors.New("the fault proxy needs a director URL")

	// errNoTransport is a default transport that is not an *http.Transport.
	errNoTransport = errors.New("the default HTTP transport is not an *http.Transport")
)

// Config is what a Proxy forwards to and which answers it loses.
type Config struct {
	// Director is the URL of the director every request is forwarded to.
	Director *url.URL

	// DirectorCAs verifies the director's certificate. Nil uses the
	// system's roots.
	DirectorCAs *x509.CertPool

	// DropDelete names the deployment whose first DELETE loses its answer.
	// An empty name drops nothing.
	DropDelete string

	// HoldLookups is how long after the drop every GET /tasks that carries
	// a context ID is answered 503. Zero holds nothing.
	HoldLookups time.Duration

	// Now is the proxy's clock. Nil uses time.Now.
	Now func() time.Time

	// Log gets a line for each request the proxy drops or refuses. Nil logs
	// to standard error.
	Log *log.Logger
}

// Proxy forwards requests to a director, except that it loses the answer
// to the first delete of one deployment, and then refuses task lookups by
// context ID for a while.
type Proxy struct {
	config  Config
	reverse *httputil.ReverseProxy

	mu        sync.Mutex
	dropTaken bool
	droppedAt time.Time
}

// dropKey marks the context of the request whose answer is dropped.
type dropKey struct{}

// NewProxy returns a Proxy for config.
func NewProxy(config Config) (*Proxy, error) {
	if config.Director == nil {
		return nil, errNoDirector
	}

	if config.Now == nil {
		config.Now = time.Now
	}

	if config.Log == nil {
		config.Log = log.New(os.Stderr, "faultproxy: ", log.LstdFlags)
	}

	defaultTransport, isTransport := http.DefaultTransport.(*http.Transport)
	if !isTransport {
		return nil, errNoTransport
	}

	transport := defaultTransport.Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: config.DirectorCAs, MinVersion: tls.VersionTLS12}

	proxy := &Proxy{config: config}
	director := config.Director

	proxy.reverse = &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(director)
		},
		Transport:      transport,
		ModifyResponse: proxy.dropAnswer,
		ErrorHandler:   proxy.handleError,
		ErrorLog:       config.Log,
	}

	return proxy, nil
}

// ServeHTTP forwards the request, unless it is a task lookup by context ID
// that the proxy holds.
func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case p.isDropTarget(request) && p.takeDrop():
		request = request.WithContext(context.WithValue(request.Context(), dropKey{}, true))
	case p.holdsLookup(request):
		p.config.Log.Printf("refused GET %s with context ID %s, because lookups by context ID are held for %s after the drop",
			request.URL.RequestURI(), request.URL.Query().Get(contextIDParameter), p.config.HoldLookups)
		http.Error(writer, "the fault proxy holds lookups by context ID", http.StatusServiceUnavailable)

		return
	}

	p.reverse.ServeHTTP(writer, request)
}

// isDropTarget reports whether the request deletes the deployment whose
// answer the proxy drops.
func (p *Proxy) isDropTarget(request *http.Request) bool {
	return p.config.DropDelete != "" &&
		request.Method == http.MethodDelete &&
		request.URL.Path == "/deployments/"+p.config.DropDelete
}

// takeDrop reports true once, for the first delete of the deployment.
func (p *Proxy) takeDrop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.dropTaken {
		return false
	}

	p.dropTaken = true

	return true
}

// holdsLookup reports whether the request is a task lookup by context ID
// within the hold that follows the drop.
func (p *Proxy) holdsLookup(request *http.Request) bool {
	if request.Method != http.MethodGet || request.URL.Path != "/tasks" ||
		!request.URL.Query().Has(contextIDParameter) {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Before the drop, droppedAt is the zero time, so its hold ended long
	// ago.
	return p.config.Now().Before(p.droppedAt.Add(p.config.HoldLookups))
}

// dropAnswer reads and throws away the director's answer to the dropped
// delete, and returns errAnswerDropped, so handleError closes the client's
// connection. Every other answer passes through.
func (p *Proxy) dropAnswer(response *http.Response) error {
	dropping, _ := response.Request.Context().Value(dropKey{}).(bool)
	if !dropping {
		return nil
	}

	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	p.mu.Lock()
	p.droppedAt = p.config.Now()
	p.mu.Unlock()

	p.config.Log.Printf("dropped the director's answer %q to DELETE %s with context ID %s",
		response.Status, response.Request.URL.Path, response.Request.Header.Get(contextIDHeader))

	return errAnswerDropped
}

// handleError closes the client's connection when the answer was dropped,
// and answers 502 when the director could not be reached.
func (p *Proxy) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, errAnswerDropped) {
		closeConnection(writer)

		return
	}

	p.config.Log.Printf("could not forward %s %s with context ID %s: %v",
		request.Method, request.URL.Path, request.Header.Get(contextIDHeader), err)
	writer.WriteHeader(http.StatusBadGateway)
}

// closeConnection closes the client's connection with no answer written.
func closeConnection(writer http.ResponseWriter) {
	hijacker, canHijack := writer.(http.Hijacker)
	if canHijack {
		conn, _, err := hijacker.Hijack()
		if err == nil {
			_ = conn.Close()

			return
		}
	}

	// The server closes the connection, or resets the stream, of a
	// handler that aborts.
	panic(http.ErrAbortHandler)
}

// generateCertificate returns a self-signed certificate for host, which is
// an IP address or a name, with its PEM. The certificate is its own CA, so
// a client can trust the PEM.
func generateCertificate(host string, now time.Time) (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("generate the key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialNumberBits))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("generate the serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "faultproxy " + host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("create the certificate: %w", err)
	}

	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, certificatePEM, nil
}

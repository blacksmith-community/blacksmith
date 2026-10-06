package http

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

const (
	dialTimeout          = 30 * time.Second
	tlsHandshakeTimeout  = 10 * time.Second
	defaultRequestWindow = 60 * time.Second
	pemBlockPrefix       = "-----BEGIN"
	certificateMarker    = "-----BEGIN CERTIFICATE-----"
	certificateType      = "CERTIFICATE"
)

// Clients holds the two HTTP clients that share one TLS-aware transport.
// JSON carries a whole-request timeout and suits small API calls. Stream has
// no whole-request timeout, so a long download is bounded by its context once
// the response headers have arrived.
type Clients struct {
	JSON   *http.Client
	Stream *http.Client
}

// NewClients builds the JSON and stream clients for the given configuration.
// It fails with boshapi.ErrInvalidCACert when CACert cannot be used.
func NewClients(cfg *boshapi.Config) (*Clients, error) {
	transport, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}

	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultRequestWindow
	}

	return &Clients{
		JSON:   &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: noRedirect},
		Stream: &http.Client{Transport: transport, CheckRedirect: noRedirect},
	}, nil
}

// noRedirect stops net/http following a redirect. The director answers a
// task-creating request with a 302 whose Location header is the result, and
// following a 307 or 308 would resend a mutation's body.
func noRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// newTransport returns a transport cloned from http.DefaultTransport that
// trusts the system roots plus cfg.CACert.
func newTransport(cfg *boshapi.Config) (*http.Transport, error) {
	return newTransportWithRoots(cfg, x509.SystemCertPool)
}

// newTransportWithRoots is newTransport with the loader of the system roots
// given. When the loader fails and CACert is set, the transport trusts
// CACert alone and cfg.Logger, if there is one, gets a warning, because a
// director signed by a private CA needs nothing else. When the loader fails
// and CACert is empty, the transport fails, so the operator sees why no
// certificate is trusted instead of an unknown-authority error on every
// request. The roots are not needed, and their failure is ignored, when
// verification is switched off.
func newTransportWithRoots(cfg *boshapi.Config, loadRoots func() (*x509.CertPool, error)) (*http.Transport, error) {
	pool, rootsErr := loadRoots()
	if rootsErr != nil {
		if !cfg.SkipTLSVerify && cfg.CACert == "" {
			return nil, fmt.Errorf("loading the system certificate roots failed (%w), so no certificate authority "+
				"would be trusted for the director. Check the operating system's trust store, such as the "+
				"ca-certificates package or the keychain, or set Config.CACert to the director's CA, or set "+
				"Config.SkipTLSVerify for a test director", rootsErr)
		}

		// Nothing the loader returned alongside its error is trusted.
		pool = nil
	}

	if pool == nil {
		pool = x509.NewCertPool()
	}

	if cfg.CACert != "" {
		err := addCACert(pool, cfg.CACert)
		if err != nil {
			return nil, err
		}

		if rootsErr != nil && !cfg.SkipTLSVerify && cfg.Logger != nil {
			cfg.Logger.Warn("loading the system certificate roots failed, so only CACert is trusted", map[string]interface{}{
				"error": rootsErr.Error(),
				"check": "the operating system's trust store, if the director's certificate is signed by a public CA",
			})
		}
	}

	headerTimeout := cfg.HTTPTimeout
	if headerTimeout <= 0 {
		headerTimeout = defaultRequestWindow
	}

	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}

	transport := base.Clone()
	transport.DialContext = (&net.Dialer{Timeout: dialTimeout, KeepAlive: dialTimeout}).DialContext
	transport.TLSHandshakeTimeout = tlsHandshakeTimeout
	transport.ResponseHeaderTimeout = headerTimeout
	transport.TLSClientConfig = &tls.Config{
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.SkipTLSVerify, // #nosec G402 -- opt-in through Config.SkipTLSVerify
	}

	return transport, nil
}

// maxCACertExcerpt is the most of a CACert value that an error quotes.
const maxCACertExcerpt = 120

// addCACert adds the PEM text, or the PEM file whose path is given, to pool.
// A value that holds a "-----BEGIN" marker anywhere is PEM text, so details
// that tools such as openssl x509 -text write before the PEM do not turn it
// into a path. Any other value is read as the path of a file.
func addCACert(pool *x509.CertPool, caCert string) error {
	pemData := []byte(caCert)

	if !strings.Contains(caCert, pemBlockPrefix) {
		data, err := os.ReadFile(caCert)
		if err != nil {
			// The cause alone: a PathError repeats the whole value.
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				err = pathErr.Err
			}

			return fmt.Errorf("%w: CACert holds no %q marker, so it was read as the file path %s, "+
				"which could not be read (%w); check that the path exists and is readable, "+
				"or pass the certificate itself as PEM text", boshapi.ErrInvalidCACert, pemBlockPrefix,
				quoteExcerpt(caCert), err)
		}

		pemData = data
	}

	return addCertificates(pool, pemData)
}

// addCertificates adds every certificate in pemData to pool. Unlike
// x509.CertPool.AppendCertsFromPEM, which skips a block it cannot read and
// reports success when any block was good, it fails when a certificate
// block is corrupt or cut short, because a CA that is silently left out
// shows up later as an unknown-authority error far from its cause.
func addCertificates(pool *x509.CertPool, pemData []byte) error {
	markers := bytes.Count(pemData, []byte(certificateMarker))
	seen, added := 0, 0

	for rest := pemData; ; {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type != certificateType || len(block.Headers) != 0 {
			continue
		}

		seen++

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("%w: certificate %d in CACert could not be parsed (%w); check that the block "+
				"is a complete, unmodified copy of the CA certificate", boshapi.ErrInvalidCACert, seen, err)
		}

		pool.AddCert(cert)

		added++
	}

	if added == 0 {
		return fmt.Errorf("%w: the PEM held no certificates; check that CACert contains "+
			"at least one %s block", boshapi.ErrInvalidCACert, certificateMarker)
	}

	if added != markers {
		return fmt.Errorf("%w: CACert holds %d %s markers, but only %d of them began a readable certificate, "+
			"so a CA would be left out; check that every block is complete, with its own BEGIN and END lines "+
			"and base64 text between them", boshapi.ErrInvalidCACert, markers, certificateMarker, added)
	}

	return nil
}

// quoteExcerpt quotes the start of text, cut to maxCACertExcerpt bytes, so a
// value that is not a path cannot flood an error message.
func quoteExcerpt(text string) string {
	if len(text) <= maxCACertExcerpt {
		return fmt.Sprintf("%q", text)
	}

	return fmt.Sprintf("%q", strings.ToValidUTF8(text[:maxCACertExcerpt], "")+"...")
}

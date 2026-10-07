// Command faultproxy sits between the broker and a BOSH director and loses
// the director's answer to one delete, so a lab can watch the broker
// settle a delete whose outcome is unknown. It listens only on a loopback
// address and uses only the standard library.
//
// Usage:
//
//	faultproxy -listen 127.0.0.1:25556 -cert-out proxy.pem \
//	  -director https://10.0.0.6:25555 -director-ca director.pem \
//	  -drop-delete <deployment> -hold-lookups 3m
//
// The proxy serves TLS with a certificate it generates for the listen
// address and writes that certificate's PEM to the -cert-out path. It
// forwards the first DELETE /deployments/<deployment> and, once the
// director answers, closes the client's connection without passing the
// answer on. With -hold-lookups, every GET /tasks that carries context_id
// gets a 503 for that long after the drop. Every other request passes
// through.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Timeouts of the proxy's server.
const (
	readHeaderTimeout = 30 * time.Second
	shutdownTimeout   = 10 * time.Second
)

// certificateFileMode lets only the proxy's user read and write the
// certificate it writes.
const certificateFileMode = 0o600

var (
	// errNotLoopback is a listen address that is not a loopback address.
	errNotLoopback = errors.New("the fault proxy listens only on a loopback address")

	// errMissingFlag is a required flag left out.
	errMissingFlag = errors.New("a required flag is missing")

	// errNoCertificates is a CA file that holds no certificate.
	errNoCertificates = errors.New("the CA file holds no PEM certificate")
)

// options are the proxy's flags.
type options struct {
	listen      string
	certOut     string
	director    string
	directorCA  string
	dropDelete  string
	holdLookups time.Duration
}

func main() {
	logger := log.New(os.Stderr, "faultproxy: ", log.LstdFlags)

	err := run(os.Args[1:], logger)
	if err != nil {
		logger.Fatal(err)
	}
}

// run parses the flags, writes the proxy's certificate, and serves until
// an interrupt or a terminate signal.
func run(args []string, logger *log.Logger) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}

	host, err := loopbackHost(opts.listen)
	if err != nil {
		return err
	}

	config, err := proxyConfig(opts, logger)
	if err != nil {
		return err
	}

	proxy, err := NewProxy(config)
	if err != nil {
		return err
	}

	certificate, certificatePEM, err := generateCertificate(host, time.Now())
	if err != nil {
		return err
	}

	err = os.WriteFile(opts.certOut, certificatePEM, certificateFileMode)
	if err != nil {
		return fmt.Errorf("write the certificate to %s: %w", opts.certOut, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", opts.listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", opts.listen, err)
	}

	logger.Printf("forwarding https://%s to %s, dropping the first delete of %q, holding lookups for %s",
		listener.Addr(), config.Director, opts.dropDelete, opts.holdLookups)

	return serve(ctx, listener, newServer(proxy, certificate))
}

// serve serves TLS on listener until ctx ends, and then shuts the server
// down.
func serve(ctx context.Context, listener net.Listener, server *http.Server) error {
	stopped := make(chan error, 1)

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		stopped <- server.Shutdown(shutdownCtx)
	}()

	err := server.ServeTLS(listener, "", "")
	if !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return <-stopped
}

// newServer returns a TLS server for handler that speaks only HTTP/1.1,
// so a dropped answer closes the client's connection.
func newServer(handler http.Handler, certificate tls.Certificate) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
}

// parseOptions parses the proxy's flags.
func parseOptions(args []string) (options, error) {
	var opts options

	flags := flag.NewFlagSet("faultproxy", flag.ContinueOnError)
	flags.StringVar(&opts.listen, "listen", "127.0.0.1:25556", "the loopback address to listen on")
	flags.StringVar(&opts.certOut, "cert-out", "", "the path to write the proxy's certificate PEM to")
	flags.StringVar(&opts.director, "director", "", "the URL of the director to forward to")
	flags.StringVar(&opts.directorCA, "director-ca", "", "the PEM file of the CA that signed the director's certificate")
	flags.StringVar(&opts.dropDelete, "drop-delete", "", "the deployment whose first delete loses its answer")
	flags.DurationVar(&opts.holdLookups, "hold-lookups", 0, "how long after the drop task lookups by context ID get a 503")

	err := flags.Parse(args)
	if err != nil {
		return options{}, fmt.Errorf("parse the flags: %w", err)
	}

	if opts.certOut == "" || opts.director == "" {
		return options{}, fmt.Errorf("%w: -cert-out and -director are required", errMissingFlag)
	}

	return opts, nil
}

// loopbackHost returns the host of a listen address, and an error unless
// it is a loopback IP address or localhost.
func loopbackHost(listen string) (string, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse the listen address %s: %w", listen, err)
	}

	if host == "localhost" {
		return host, nil
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("%w: %s", errNotLoopback, listen)
	}

	return host, nil
}

// proxyConfig builds the proxy's configuration from the flags.
func proxyConfig(opts options, logger *log.Logger) (Config, error) {
	director, err := url.Parse(opts.director)
	if err != nil {
		return Config{}, fmt.Errorf("parse the director URL %s: %w", opts.director, err)
	}

	var pool *x509.CertPool

	if opts.directorCA != "" {
		caPEM, err := os.ReadFile(opts.directorCA)
		if err != nil {
			return Config{}, fmt.Errorf("read the director CA %s: %w", opts.directorCA, err)
		}

		pool = x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return Config{}, fmt.Errorf("%w: %s", errNoCertificates, opts.directorCA)
		}
	}

	return Config{
		Director:    director,
		DirectorCAs: pool,
		DropDelete:  opts.dropDelete,
		HoldLookups: opts.holdLookups,
		Log:         logger,
	}, nil
}

package certinventory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"time"
)

// DefaultProbeTimeout covers the dial and the handshake together.
const DefaultProbeTimeout = 5 * time.Second

// Prober fetches the certificate a TLS listener presents.
type Prober struct {
	// Timeout bounds the dial and the handshake together. Zero means five
	// seconds.
	Timeout time.Duration
	// RootCAs is the pool a caller hands to VerifyChain after a probe. The
	// handshake itself never verifies, so a listener with a bad chain still
	// gives up its certificate.
	RootCAs *x509.CertPool
}

// Leaf dials addr, sends serverName for SNI, and returns the leaf the server
// presents along with the rest of its chain. It skips verification and
// captures the chain in a callback, so a server that then rejects the client
// for a missing certificate still yields its leaf. When no certificate
// arrives, whether from a refused connection, a timeout, or a peer that does
// not speak TLS, it returns an error and no certificate.
func (p Prober) Leaf(ctx context.Context, addr, serverName string) (*x509.Certificate, []*x509.Certificate, error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var raw [][]byte

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{},
		Config: probeTLSConfig(serverName, func(rawCerts [][]byte) {
			raw = make([][]byte, len(rawCerts))
			for i, c := range rawCerts {
				raw[i] = append([]byte(nil), c...)
			}
		}),
	}

	conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
	if conn != nil {
		_ = conn.Close()
	}

	if len(raw) == 0 {
		if dialErr == nil {
			dialErr = errors.New("the server presented no certificate")
		}

		return nil, nil, fmt.Errorf("probe %s: %w", addr, dialErr)
	}

	certs := make([]*x509.Certificate, 0, len(raw))

	for _, der := range raw {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, nil, fmt.Errorf("probe %s: parse presented certificate: %w", addr, err)
		}

		certs = append(certs, c)
	}

	return certs[0], certs[1:], nil
}

// VerifyChain checks that leaf chains to roots at the given time, using the
// intermediates as needed. Any extended key usage is accepted, so the same
// check serves server and client certificates.
func VerifyChain(leaf *x509.Certificate, intermediates []*x509.Certificate, roots *x509.CertPool, at time.Time) error {
	if leaf == nil {
		return errors.New("no certificate to verify")
	}

	pool := x509.NewCertPool()
	for _, c := range intermediates {
		pool.AddCert(c)
	}

	_, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: pool,
		CurrentTime:   at,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return fmt.Errorf("verify chain: %w", err)
	}

	return nil
}

// probeTLSConfig builds the client configuration for a probe. It skips
// verification and hands the presented chain to capture. It turns session
// tickets off and leaves the client session cache unset, so every probe does
// a full handshake and a resumed session can never skip the certificate the
// server presents.
func probeTLSConfig(serverName string, capture func([][]byte)) *tls.Config {
	return &tls.Config{
		ServerName: serverName,
		//nolint:gosec // The probe reads the presented certificate; it does not trust it.
		InsecureSkipVerify: true,
		// The floor is TLS 1.2, so a listener that only offers TLS 1.0 or
		// 1.1 fails the handshake and reports as unreachable.
		MinVersion:             tls.VersionTLS12,
		SessionTicketsDisabled: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			capture(rawCerts)

			return nil
		},
	}
}

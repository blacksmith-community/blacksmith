package certificates

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// FetchCertificateFromEndpoint connects to a network endpoint and retrieves its certificate.
func FetchCertificateFromEndpoint(ctx context.Context, address string, timeout time.Duration) (*CertificateInfo, error) {
	return FetchCertificateFromEndpointWithVerify(ctx, address, timeout, true) // Default to skip verify for certificate inspection
}

// parseNetworkAddress parses various address formats and returns a normalized host:port address.
func parseNetworkAddress(address string) (string, error) {
	var host, port string

	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		parsedURL, err := url.Parse(address)
		if err != nil {
			return "", fmt.Errorf("invalid URL format: %w", err)
		}

		host = parsedURL.Hostname()
		port = parsedURL.Port()

		if port == "" {
			switch parsedURL.Scheme {
			case "https":
				port = "443"
			case "http":
				port = "80"
			default:
				port = "443"
			}
		}
	} else {
		var err error

		host, port, err = net.SplitHostPort(address)
		if err != nil {
			host = address
			port = "443"
		}
	}

	return net.JoinHostPort(host, port), nil
}

// establishTLSConnection creates a TLS connection to the specified address.
func establishTLSConnection(ctx context.Context, address string, timeout time.Duration, skipTLSVerify bool) (*tls.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: timeout},
		Config: &tls.Config{
			InsecureSkipVerify: skipTLSVerify, // #nosec G402 - Configurable TLS verification for certificate inspection
		},
	}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", address, err)
	}

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close()

		return nil, ErrConnectionIsNotTLS
	}

	return tlsConn, nil
}

// buildCertificateChain builds certificate chain information from peer certificates.
func buildCertificateChain(peerCertificates []*x509.Certificate) []CertificateChainInfo {
	if len(peerCertificates) <= 1 {
		return nil
	}

	const maxChainLength = 9

	// Pre-allocate with capacity based on the smaller of peer certificates or max chain length
	chainCapacity := len(peerCertificates) - 1
	if chainCapacity > maxChainLength {
		chainCapacity = maxChainLength
	}

	chain := make([]CertificateChainInfo, 0, chainCapacity)

	for chainIndex, chainCert := range peerCertificates[1:] {
		if chainIndex >= maxChainLength {
			break
		}

		chainInfo := CertificateChainInfo{
			Subject:      chainCert.Subject.String(),
			Issuer:       chainCert.Issuer.String(),
			SerialNumber: chainCert.SerialNumber.String(),
			NotBefore:    chainCert.NotBefore,
			NotAfter:     chainCert.NotAfter,
		}
		chain = append(chain, chainInfo)
	}

	return chain
}

// FetchCertificateFromEndpointWithVerify connects to a network endpoint and retrieves its certificate with TLS verification control.
func FetchCertificateFromEndpointWithVerify(ctx context.Context, address string, timeout time.Duration, skipTLSVerify bool) (*CertificateInfo, error) {
	normalizedAddress, err := parseNetworkAddress(address)
	if err != nil {
		return nil, err
	}

	tlsConn, err := establishTLSConnection(ctx, normalizedAddress, timeout, skipTLSVerify)
	if err != nil {
		return nil, err
	}

	defer func() { _ = tlsConn.Close() }()

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoCertificatesFoundForAddress, address)
	}

	cert := state.PeerCertificates[0]
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  CertificateBlockType,
		Bytes: cert.Raw,
	})

	certInfo, err := ParseCertificateFromX509(ctx, cert, string(pemData))
	if err != nil {
		return nil, err
	}

	certInfo.Chain = buildCertificateChain(state.PeerCertificates)

	return certInfo, nil
}

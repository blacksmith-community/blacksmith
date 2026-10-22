package certinventory

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// Finding codes the parser can raise.
const (
	FindingWeakKey       = "weak_key"
	FindingWeakSignature = "weak_signature"
)

// CertFacts holds what a certificate says about itself.
type CertFacts struct {
	CommonName     string    `json:"common_name,omitempty"`
	Issuer         string    `json:"issuer,omitempty"`
	SubjectKeyID   string    `json:"subject_key_id,omitempty"`
	AuthorityKeyID string    `json:"authority_key_id,omitempty"`
	DNSNames       []string  `json:"dns_names,omitempty"`
	IPAddresses    []string  `json:"ip_addresses,omitempty"`
	ExtKeyUsages   []string  `json:"ext_key_usages,omitempty"`
	KeyAlgorithm   string    `json:"key_algorithm,omitempty"`
	KeyBits        int       `json:"key_bits,omitempty"`
	Serial         string    `json:"serial,omitempty"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
}

// ParsePEMCertificates returns every CERTIFICATE block in order. It skips
// other block types without reporting them, and a block that does not parse
// is skipped too. It fails only when no certificate parses.
func ParsePEMCertificates(pemText []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate

	rest := pemText

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type != "CERTIFICATE" {
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}

		certs = append(certs, cert)
	}

	if len(certs) == 0 {
		return nil, errors.New("no certificate found in PEM text")
	}

	return certs, nil
}

// Fingerprint returns the lowercase hex SHA-256 of the certificate's DER.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)

	return hex.EncodeToString(sum[:])
}

// KindOf says whether c is a root CA, an intermediate, or a leaf.
func KindOf(c *x509.Certificate) Kind {
	if !c.BasicConstraintsValid || !c.IsCA {
		return KindLeaf
	}

	if bytes.Equal(c.RawSubject, c.RawIssuer) {
		return KindCA
	}

	return KindIntermediate
}

var extKeyUsageNames = map[x509.ExtKeyUsage]string{
	x509.ExtKeyUsageAny:                            "any",
	x509.ExtKeyUsageServerAuth:                     "serverAuth",
	x509.ExtKeyUsageClientAuth:                     "clientAuth",
	x509.ExtKeyUsageCodeSigning:                    "codeSigning",
	x509.ExtKeyUsageEmailProtection:                "emailProtection",
	x509.ExtKeyUsageIPSECEndSystem:                 "ipsecEndSystem",
	x509.ExtKeyUsageIPSECTunnel:                    "ipsecTunnel",
	x509.ExtKeyUsageIPSECUser:                      "ipsecUser",
	x509.ExtKeyUsageTimeStamping:                   "timeStamping",
	x509.ExtKeyUsageOCSPSigning:                    "ocspSigning",
	x509.ExtKeyUsageMicrosoftServerGatedCrypto:     "microsoftServerGatedCrypto",
	x509.ExtKeyUsageNetscapeServerGatedCrypto:      "netscapeServerGatedCrypto",
	x509.ExtKeyUsageMicrosoftCommercialCodeSigning: "microsoftCommercialCodeSigning",
	x509.ExtKeyUsageMicrosoftKernelCodeSigning:     "microsoftKernelCodeSigning",
}

// Facts extracts the identifying fields of c.
func Facts(c *x509.Certificate) CertFacts {
	f := CertFacts{
		CommonName:     c.Subject.CommonName,
		Issuer:         c.Issuer.String(),
		SubjectKeyID:   hex.EncodeToString(c.SubjectKeyId),
		AuthorityKeyID: hex.EncodeToString(c.AuthorityKeyId),
		DNSNames:       append([]string(nil), c.DNSNames...),
		Serial:         c.SerialNumber.Text(16),
		NotBefore:      c.NotBefore,
		NotAfter:       c.NotAfter,
	}

	for _, ip := range c.IPAddresses {
		f.IPAddresses = append(f.IPAddresses, ip.String())
	}

	for _, u := range c.ExtKeyUsage {
		if name, ok := extKeyUsageNames[u]; ok {
			f.ExtKeyUsages = append(f.ExtKeyUsages, name)
		} else {
			f.ExtKeyUsages = append(f.ExtKeyUsages, fmt.Sprintf("unknown(%d)", int(u)))
		}
	}

	f.KeyAlgorithm, f.KeyBits = keyInfo(c)

	return f
}

func keyInfo(c *x509.Certificate) (string, int) {
	switch k := c.PublicKey.(type) {
	case *rsa.PublicKey:
		return "RSA", k.N.BitLen()
	case *ecdsa.PublicKey:
		return "ECDSA", k.Curve.Params().BitSize
	case ed25519.PublicKey:
		return "Ed25519", 256
	default:
		return c.PublicKeyAlgorithm.String(), 0
	}
}

// WeaknessFindings reports a weak key or a weak signature algorithm.
func WeaknessFindings(c *x509.Certificate) []Finding {
	var out []Finding

	algo, bits := keyInfo(c)

	if (algo == "RSA" && bits < 2048) || (algo == "ECDSA" && bits < 256) {
		out = append(out, Finding{
			Code:     FindingWeakKey,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("The %s key is only %d bits long, which is below today's minimum.", algo, bits),
		})
	}

	switch c.SignatureAlgorithm {
	case x509.MD2WithRSA, x509.MD5WithRSA, x509.SHA1WithRSA, x509.DSAWithSHA1, x509.ECDSAWithSHA1:
		out = append(out, Finding{
			Code:     FindingWeakSignature,
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("The certificate is signed with %s, which is no longer considered safe.", c.SignatureAlgorithm),
		})
	}

	return out
}

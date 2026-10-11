package sshconn

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

var (
	// ErrHostKeyMismatch means a VM presented a host key other than the one
	// the director reported for it.
	ErrHostKeyMismatch = errors.New("the VM's host key is not the one the director reported")
	// ErrHostKeyUnusable means the director reported no host key for an
	// instance, or one that does not parse, so Blacksmith cannot check whom
	// it connects to.
	ErrHostKeyUnusable = errors.New("the director reported no usable host key")
)

// hostKeyNegotiation is what x/crypto's AlgorithmNegotiationError names
// when the client and the server share no host key algorithm
// (vendor/golang.org/x/crypto/ssh/common.go, findAgreedAlgorithms).
const hostKeyNegotiation = "host key"

// HostKeyMismatchError is the refusal of a VM whose host key is not the one
// the director reported at setup. The VM either presented another key, or
// offered no key of the type the director reported.
type HostKeyMismatchError struct {
	// Deployment and Instance name the VM, when the caller knows them.
	Deployment string
	Instance   string
	// Host is the address the client dialled.
	Host string
	// Want is the SHA256 fingerprint of the key the director reported,
	// and Got is that of the key the VM presented. Got is empty when the
	// VM offered no key of the reported type.
	Want string
	Got  string
	// WantType is the type of the key the director reported, and Offered
	// lists the host key algorithms the VM offered, when it offered none
	// that the reported key signs with.
	WantType string
	Offered  []string
	// Err is the handshake's own error, when the VM offered no key of the
	// reported type.
	Err error
}

func (e *HostKeyMismatchError) Error() string {
	named := e.Host
	if e.Instance != "" {
		named = fmt.Sprintf("%s (%s of deployment %s)", e.Host, e.Instance, e.Deployment)
	}

	const meaning = "The VM may have been recreated since the setup, or something is intercepting the connection"

	if e.Got == "" {
		return fmt.Sprintf("%s: %s offered only host key algorithms %s, and none of them is for the %s key %s the director reported at setup. %s",
			ErrHostKeyMismatch, named, strings.Join(e.Offered, ", "), e.WantType, e.Want, meaning)
	}

	return fmt.Sprintf("%s: %s presented host key %s, but the director reported %s at setup. %s",
		ErrHostKeyMismatch, named, e.Got, e.Want, meaning)
}

// Unwrap lets errors.Is match ErrHostKeyMismatch, and the handshake's own
// error when there is one.
func (e *HostKeyMismatchError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrHostKeyMismatch}
	}

	return []error{ErrHostKeyMismatch, e.Err}
}

// ExplainHostKeyRefusal returns err as a *HostKeyMismatchError when the
// SSH handshake with addr failed because the VM offered no host key of the
// type of host's pinned key, and returns err as it is otherwise. The
// pinned configuration asks only for the reported key's algorithms, so
// x/crypto ends such a handshake in algorithm negotiation before the host
// key callback runs, and its error names no instance and no fingerprint.
// A VM that offers the reported key's type, but only with algorithms the
// pinned configuration leaves out, such as SHA-1 RSA, may hold the
// reported key, so its failure is left as it is.
func ExplainHostKeyRefusal(err error, deployment string, host Host, addr string) error {
	var negotiation *ssh.AlgorithmNegotiationError
	if err == nil || host.HostKey == nil || !errors.As(err, &negotiation) || negotiation.What != hostKeyNegotiation {
		return err
	}

	for _, offered := range negotiation.RequestedAlgorithms {
		if slices.Contains(keyTypeAlgorithms(host.HostKey), offered) {
			return err
		}
	}

	return &HostKeyMismatchError{
		Deployment: deployment,
		Instance:   host.Instance(),
		Host:       addr,
		Want:       ssh.FingerprintSHA256(host.HostKey),
		WantType:   host.HostKey.Type(),
		Offered:    slices.Clone(negotiation.RequestedAlgorithms),
		Err:        err,
	}
}

// PinnedConfig returns the client configuration that logs in as user with
// signer and accepts only hostKey. Its HostKeyAlgorithms list only the
// algorithms hostKey signs with, so the handshake asks the server for that
// key. A VM's sshd holds RSA, ECDSA, and Ed25519 host keys while the
// director reports only the RSA one, and without the narrowed list the
// client's default order would negotiate ECDSA and refuse every VM. The
// caller sets Timeout.
func PinnedConfig(user string, signer ssh.Signer, hostKey ssh.PublicKey) *ssh.ClientConfig {
	return pinnedConfig(user, signer, hostKey, "", "")
}

// HostConfig is PinnedConfig for one host of a session, whose refusal of a
// mismatched key names the deployment and the instance. A host with no key
// is refused, because it could only be reached unchecked.
func HostConfig(deployment string, host Host, signer ssh.Signer) (*ssh.ClientConfig, error) {
	if host.HostKey == nil {
		return nil, fmt.Errorf("%w: the director reported no host key for %s of deployment %s, so Blacksmith will not connect to %s",
			ErrHostKeyUnusable, host.Instance(), deployment, host.Address)
	}

	return pinnedConfig(host.Username, signer, host.HostKey, deployment, host.Instance()), nil
}

func pinnedConfig(user string, signer ssh.Signer, hostKey ssh.PublicKey, deployment, instance string) *ssh.ClientConfig {
	pinned := hostKey.Marshal()
	want := ssh.FingerprintSHA256(hostKey)

	return &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(hostname string, _ net.Addr, key ssh.PublicKey) error {
			if bytes.Equal(key.Marshal(), pinned) {
				return nil
			}

			return &HostKeyMismatchError{
				Deployment: deployment,
				Instance:   instance,
				Host:       hostname,
				Want:       want,
				Got:        ssh.FingerprintSHA256(key),
			}
		},
		HostKeyAlgorithms: hostKeyAlgorithms(hostKey),
	}
}

// hostKeyAlgorithms lists the host key algorithms that verify a signature
// by key. An RSA key, or an RSA certificate, gets the SHA-2 algorithms
// alone. The SHA-1 ssh-rsa algorithm is left out, because x/crypto marks
// it insecure and OpenSSH 8.8 and later refuse it by default. Any other
// key signs only with its own type.
func hostKeyAlgorithms(key ssh.PublicKey) []string {
	switch key.Type() {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	case ssh.CertAlgoRSAv01:
		return []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01}
	default:
		return []string{key.Type()}
	}
}

// keyTypeAlgorithms lists every host key algorithm that signs with a key
// of key's type, the SHA-1 one included, so a server that offers any of
// them holds a key of that type.
func keyTypeAlgorithms(key ssh.PublicKey) []string {
	switch key.Type() {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	case ssh.CertAlgoRSAv01:
		return []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01}
	default:
		return []string{key.Type()}
	}
}

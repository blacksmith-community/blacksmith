package sshconn

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/ssh"
)

// userPrefix starts every throwaway user name. The BOSH agent's cleanup
// deletes only users whose names start with it.
const userPrefix = "bosh_"

// userSuffixLength is how many hex characters follow the prefix. Windows
// caps a user name at 20 characters, and bosh-cli uses the same length.
const userSuffixLength = 15

// Credentials is the throwaway login for one SSH setup. The private key
// lives only in Signer, in memory, and is never written anywhere.
type Credentials struct {
	// User is the user the director creates on the VM, bosh_ followed by
	// 15 lowercase hex characters.
	User string
	// PublicKey is the public key in authorized-keys form, which the
	// setup request carries.
	PublicKey string
	// Signer signs the login with the private key.
	Signer ssh.Signer
}

// NewCredentials makes a fresh ed25519 key and a fresh user name from
// crypto/rand.
func NewCredentials() (*Credentials, error) {
	return newCredentials(rand.Reader)
}

func newCredentials(random io.Reader) (*Credentials, error) {
	user, err := newUserName(random)
	if err != nil {
		return nil, err
	}

	public, private, err := ed25519.GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("generate the SSH key for user %s: %w", user, err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return nil, fmt.Errorf("make the SSH signer for user %s: %w", user, err)
	}

	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, fmt.Errorf("encode the SSH public key for user %s: %w", user, err)
	}

	return &Credentials{
		User:      user,
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey))),
		Signer:    signer,
	}, nil
}

// newUserName returns bosh_ followed by 15 random lowercase hex
// characters.
func newUserName(random io.Reader) (string, error) {
	suffix := make([]byte, hex.DecodedLen(userSuffixLength+1))

	_, err := io.ReadFull(random, suffix)
	if err != nil {
		return "", fmt.Errorf("draw a random SSH user name: %w", err)
	}

	return userPrefix + hex.EncodeToString(suffix)[:userSuffixLength], nil
}

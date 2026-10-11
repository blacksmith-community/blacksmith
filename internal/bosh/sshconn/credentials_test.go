package sshconn_test

import (
	"bytes"
	"regexp"
	"testing"

	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// agentUserForm is the user name form the BOSH agent's cleanup removes,
// within the 20 characters Windows allows.
var agentUserForm = regexp.MustCompile(`^bosh_[0-9a-f]{15}$`)

// The throwaway user keeps the form bosh-cli uses, because the agent's
// cleanup deletes only users whose names start with bosh_, and Windows caps
// a user name at 20 characters. Every draw is a new name.
func TestSSHUserNameKeepsTheAgentsForm(t *testing.T) {
	t.Parallel()

	const draws = 1000

	seen := make(map[string]bool, draws)

	for range draws {
		credentials, err := sshconn.NewCredentials()
		if err != nil {
			t.Fatalf("NewCredentials: %v", err)
		}

		if !agentUserForm.MatchString(credentials.User) {
			t.Fatalf("user %q does not match %s", credentials.User, agentUserForm)
		}

		if len(credentials.User) > 20 {
			t.Fatalf("user %q is %d characters long, more than the 20 Windows allows", credentials.User, len(credentials.User))
		}

		if seen[credentials.User] {
			t.Fatalf("user %q was drawn twice in %d draws", credentials.User, draws)
		}

		seen[credentials.User] = true
	}
}

// The throwaway key is ed25519, and the public key in authorized-keys form
// is the signer's own.
func TestSSHCredentialsAreEd25519(t *testing.T) {
	t.Parallel()

	credentials, err := sshconn.NewCredentials()
	if err != nil {
		t.Fatalf("NewCredentials: %v", err)
	}

	if got := credentials.Signer.PublicKey().Type(); got != ssh.KeyAlgoED25519 {
		t.Fatalf("signer key type %q, want %q", got, ssh.KeyAlgoED25519)
	}

	sent, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(credentials.PublicKey))
	if err != nil {
		t.Fatalf("public key %q is not in authorized-keys form: %v", credentials.PublicKey, err)
	}

	if len(bytes.TrimSpace(rest)) != 0 {
		t.Fatalf("public key %q holds more than one key", credentials.PublicKey)
	}

	if sent.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("public key type %q, want %q", sent.Type(), ssh.KeyAlgoED25519)
	}

	if !bytes.Equal(sent.Marshal(), credentials.Signer.PublicKey().Marshal()) {
		t.Fatal("the public key the setup sends is not the signer's public key")
	}
}

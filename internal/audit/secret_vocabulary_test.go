package audit_test

import (
	"testing"

	"blacksmith/internal/audit"
)

// The short and joined spellings of a secret count as one, and so does any
// key with a letter outside ASCII, since a look-alike letter could spell a
// secret that the word list would miss.
func TestSecretKeyCatchesShortJoinedAndLookAlikeSpellings(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"pw", "admin_pw", "adminPw", "passcode", "admin_passcode", "privkey", "ssh_privkey", "sshkey", "masterkey",
		"authkey", "sessionid", "session_id", "client_assertion", "pаssword", "naïve",
	} {
		if !audit.SecretKey(key) {
			t.Errorf("SecretKey(%q) = false, want true", key)
		}
	}

	for _, key := range []string{"power", "pwr_state", keyAPIURL, "username", "broker_name", "auto_register"} {
		if audit.SecretKey(key) {
			t.Errorf("SecretKey(%q) = true, want false", key)
		}
	}
}

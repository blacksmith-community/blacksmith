package ssh_test

import (
	"strings"
	"testing"

	boshssh "blacksmith/internal/bosh/ssh"
)

// testKeyBody is 64 characters of base64, as one line of a PEM body.
const testKeyBody = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"

// testHash is a SHA-256 hash in lowercase hex.
const testHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestWithheld(t *testing.T) {
	t.Parallel()

	keyBlock := "-----BEGIN PRIVATE KEY-----\n" + testKeyBody + "\n-----END PRIVATE KEY-----\n"

	cases := map[string]struct {
		stream string
		want   bool
	}{
		"a whole key block":                        {keyBlock, true},
		"a stream cut after the marker starts":     {"note: copied\n-----BEG", true},
		"a stream that starts mid-body":            {testKeyBody + "\n", true},
		"a key name with BEGIN alone":              {"BEGIN", true},
		"PRIVATE alone":                            {"PRIVATE", true},
		"a hash":                                   {"hash=" + testHash, false},
		"a state line":                             {"state=old path=/var/vcap/data/jobs/x/y", false},
		"39 base64 characters":                     {strings.Repeat("A", 39), false},
		"40 base64 characters":                     {strings.Repeat("A", 40), true},
		"a long uppercase hex run":                 {strings.ToUpper(testHash), true},
		"empty":                                    {"", false},
		"a hash with a key body later in the text": {"hash=" + testHash + " " + testKeyBody, true},
	}

	for name, testCase := range cases {
		if got := boshssh.Withheld([]byte(testCase.stream)); got != testCase.want {
			t.Errorf("%s: Withheld(%q) = %v, want %v", name, testCase.stream, got, testCase.want)
		}
	}
}

// ForLog names the size of a withheld stream and passes any other through.
func TestForLog(t *testing.T) {
	t.Parallel()

	stream := "-----BEGIN PRIVATE KEY-----\n" + testKeyBody + "\n"
	if got, want := boshssh.ForLog([]byte(stream)), "[output withheld, 93 bytes]"; got != want {
		t.Errorf("ForLog of a key = %q, want %q", got, want)
	}

	if got, want := boshssh.ForLog([]byte("hash="+testHash)), "hash="+testHash; got != want {
		t.Errorf("ForLog of a hash = %q, want %q", got, want)
	}
}

// ForLog escapes line breaks, tabs, backslashes, and characters a
// terminal does not print, so text a VM printed can never start a log
// line of its own.
func TestForLogEscapesControlCharacters(t *testing.T) {
	t.Parallel()

	for stream, want := range map[string]string{
		"state=old\nINFO forged line": `state=old\nINFO forged line`,
		"done\rINFO overwritten":      `done\rINFO overwritten`,
		"a\tb":                        `a\tb`,
		`C:\path`:                     `C:\\path`,
		"line\u2028separator":         `line\u2028separator`,
		"plain text, déjà vu":         "plain text, déjà vu",
	} {
		if got := boshssh.ForLog([]byte(stream)); got != want {
			t.Errorf("ForLog(%q) = %q, want %q", stream, got, want)
		}
	}
}

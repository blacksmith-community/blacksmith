package ssh_test

import (
	"fmt"
	"strings"
	"testing"

	boshssh "blacksmith/internal/bosh/ssh"
)

// testKeyBody is 64 characters of base64, as one line of a PEM body.
const testKeyBody = "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7VJTUt9Us8cKj"

// testHash is a SHA-256 hash in lowercase hex.
const testHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

// colonHex renders count hex byte pairs separated by colons, as openssl
// prints key material, with perLine pairs on each indented line.
func colonHex(count int, upper bool, perLine int) string {
	var out strings.Builder

	for pairIndex := range count {
		if pairIndex%perLine == 0 {
			out.WriteString("\n    ")
		}

		pair := fmt.Sprintf("%02x", (pairIndex*37+11)%256)
		if upper {
			pair = strings.ToUpper(pair)
		}

		out.WriteString(pair)

		if pairIndex < count-1 {
			out.WriteString(":")
		}
	}

	return out.String()
}

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
		"text with line breaks and tabs":           {"state=old\r\n\tpath=/var/vcap/data/x\n", false},
		"raw DER that is not UTF-8":                {"\x30\x82\x04\xbd\x02\x01\x00", true},
		"raw DER that is valid UTF-8":              {"\x30\x13\x02\x01\x05", true},
		"a NUL":                                    {"ok\x00", true},
		"an ANSI escape sequence":                  {"\x1b[31mred", true},
		"DEL":                                      {"ok\x7f", true},
		"a C1 control character":                   {"ok\u0085", true},
		"15 colon-separated hex pairs":             {colonHex(15, false, 15), false},
		"16 colon-separated hex pairs":             {colonHex(16, false, 16), true},
		"16 uppercase colon-separated hex pairs":   {colonHex(16, true, 16), true},
		"an openssl pkey -text dump":               {"Private-Key: (2048 bit, 2 primes)\nmodulus:\n" + colonHex(45, false, 15), true},
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

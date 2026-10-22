package ssh_test

import (
	"testing"

	boshssh "blacksmith/internal/bosh/ssh"
)

// quotedWord is a word with a single quote in it.
const quotedWord = "it's"

func TestQuote(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"a b":                    `'a b'`,
		quotedWord:               `'it'\''s'`,
		"":                       `''`,
		"/var/vcap/data/x":       `'/var/vcap/data/x'`,
		`$(reboot) "x" ` + "`y`": `'$(reboot) "x" ` + "`y`'",
	} {
		if got := boshssh.Quote(input); got != want {
			t.Errorf("Quote(%q) = %q, want %q", input, got, want)
		}
	}
}

// Command keeps the program as written and quotes every argument.
func TestCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		program string
		args    []string
		want    string
	}{
		{"sudo -n /bin/bash -s --", []string{"stage", quotedWord, ""}, `sudo -n /bin/bash -s -- 'stage' 'it'\''s' ''`},
		{"/bin/true", nil, "/bin/true"},
	}

	for _, testCase := range cases {
		if got := boshssh.Command(testCase.program, testCase.args); got != testCase.want {
			t.Errorf("Command(%q, %q) = %q, want %q", testCase.program, testCase.args, got, testCase.want)
		}
	}
}

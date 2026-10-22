package ssh

import "testing"

// escapeForLog writes every C0 and C1 control character, DEL, and any
// byte that is not UTF-8 as an escape, so none of them reaches a log raw.
func TestEscapeForLogEscapesEveryControlCharacter(t *testing.T) {
	t.Parallel()

	for stream, want := range map[string]string{
		"\x1b[31mred\x1b[0m": `\x1b[31mred\x1b[0m`,
		"nul\x00":            `nul\x00`,
		"bell\a del\x7f":     `bell\x07 del\x7f`,
		"c1\u0085next":       `c1\u0085next`,
		"csi\u009b2J":        `csi\u009b2J`,
		"bad\xffutf8":        `bad\xffutf8`,
		"ok":                 "ok",
	} {
		if got := escapeForLog([]byte(stream)); got != want {
			t.Errorf("escapeForLog(%q) = %q, want %q", stream, got, want)
		}
	}
}

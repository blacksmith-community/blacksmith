package ssh

import "strings"

// Quote wraps s in POSIX single quotes, so a shell reads it back as one
// word with every character taken literally. A single quote inside s
// becomes a closing quote, a backslash-escaped quote, and an opening
// quote, which is the only way to put one inside single-quoted text.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Command joins program and its arguments into the command line an SSH
// exec request carries. The program is fixed text the caller wrote and
// is kept as it is. Every argument is quoted, so a path or a name can
// never be read as more than one word or as shell syntax.
func Command(program string, args []string) string {
	if len(args) == 0 {
		return program
	}

	words := make([]string, 0, len(args)+1)
	words = append(words, program)

	for _, arg := range args {
		words = append(words, Quote(arg))
	}

	return strings.Join(words, " ")
}

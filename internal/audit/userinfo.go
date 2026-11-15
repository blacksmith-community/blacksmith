package audit

import "strings"

// maxPortDigits is the most digits a port has.
const maxPortDigits = 5

// stripURLPassword replaces the password in the userinfo of each word of
// a value with Changed, and keeps its username and host, so a pasted URL
// never stores a credential. Words break at ASCII white space and at
// double quotes, so a URL inside JSON text is found too. The page applies
// the same rule as a second guard, and both are tested against
// testdata/url-passwords.json.
func stripURLPassword(value string) string {
	if !strings.Contains(value, "@") {
		return value
	}

	var out strings.Builder

	start := 0

	for index := 0; index <= len(value); index++ {
		if index < len(value) && !wordBreak(value[index]) {
			continue
		}

		out.WriteString(stripWordPassword(value[start:index]))

		if index < len(value) {
			out.WriteByte(value[index])
		}

		start = index + 1
	}

	return out.String()
}

// wordBreak reports whether a byte ends a word for stripURLPassword.
func wordBreak(char byte) bool {
	switch char {
	case ' ', '\t', '\n', '\v', '\f', '\r', '"':
		return true
	default:
		return false
	}
}

// stripWordPassword hides the password in one word that holds userinfo,
// with a scheme or without one. The userinfo ends at the word's last @,
// so a password may hold a raw /, ?, #, or @. The username runs to the
// first colon, and holds none of /, ?, #, or @. A word whose part after
// that colon is a port followed by a path, query, or fragment is a host
// and port, and is left alone, and so is a word with no password.
func stripWordPassword(word string) string {
	userEnd := strings.LastIndexByte(word, '@')
	if userEnd < 0 {
		return word
	}

	start := 0
	if scheme := strings.Index(word, "://"); scheme >= 0 && scheme < userEnd {
		start = scheme + len("://")
	}

	colon := strings.IndexByte(word[start:userEnd], ':')
	if colon < 0 {
		return word
	}

	colon += start

	if strings.ContainsAny(word[start:colon], "/?#@") {
		return word
	}

	password := word[colon+1 : userEnd]
	if password == "" {
		return word
	}

	if cut := strings.IndexAny(password, "/?#"); cut >= 0 && portDigits(password[:cut]) {
		return word
	}

	return word[:colon+1] + Changed + word[userEnd:]
}

// portDigits reports whether text is one to five ASCII digits, as a port
// is.
func portDigits(text string) bool {
	if text == "" || len(text) > maxPortDigits {
		return false
	}

	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}

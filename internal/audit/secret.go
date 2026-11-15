package audit

import (
	"strings"
	"unicode"
)

// Changed replaces both values of a change whose key names a secret.
const Changed = "(changed)"

// SecretKey reports whether a setting's key names a secret, such as a
// password, a token, a key, a credential, or a certificate's private key.
// It looks at the key alone and never at a value, so a secret is hidden
// whatever it holds. A dotted key, as a nested setting has, is a secret
// when any part of it names one. A key with any character outside ASCII
// is a secret too, because a look-alike letter, such as a Cyrillic a in
// "password", would slip past the word list.
func SecretKey(key string) bool {
	if !asciiOnly(key) {
		return true
	}

	words := keyWords(key)

	for _, word := range words {
		if secretWord(word) {
			return true
		}
	}

	joined := strings.Join(words, "")

	for _, fragment := range secretFragments() {
		if strings.Contains(joined, fragment) {
			return true
		}
	}

	return false
}

// secretWord reports whether one word of a key, by itself, names a secret.
func secretWord(word string) bool {
	switch word {
	case "key", "keys", "pass", "pw", "pwd", "pin", "cred", "creds", "pem", "jwt", "otp", "seed", "salt", "nonce", "auth", "authorization", "cookie", "session":
		return true
	default:
		return false
	}
}

// secretFragments lists the runs of letters that name a secret wherever
// they fall in a key, so "clientsecret", "access_token", and "privateKey"
// all count.
func secretFragments() []string {
	return []string{
		"password", "passwd", "passphrase", "secret", "token", "credential",
		"privatekey", "apikey", "accesskey", "signingkey", "encryptionkey",
		"unsealkey", "bearer", "passcode", "privkey", "sshkey", "masterkey",
		"authkey", "session", "assertion",
	}
}

// asciiOnly reports whether every character of the key is ASCII.
func asciiOnly(key string) bool {
	for _, char := range key {
		if char > unicode.MaxASCII {
			return false
		}
	}

	return true
}

// keyWords splits a key into lower-case words at every character that is
// not a letter or a digit and at each change from lower to upper case.
func keyWords(key string) []string {
	var (
		words   []string
		current strings.Builder
		last    rune
	)

	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}

	for _, char := range key {
		switch {
		case !unicode.IsLetter(char) && !unicode.IsDigit(char):
			flush()
		case unicode.IsUpper(char) && unicode.IsLower(last):
			flush()
			current.WriteRune(unicode.ToLower(char))
		default:
			current.WriteRune(unicode.ToLower(char))
		}

		last = char
	}

	flush()

	return words
}

package audit

import "regexp"

// urlPassword finds the password in the userinfo of a URL anywhere in a
// value. The username runs to the first colon, and the password runs to
// the last @ before the path, query, fragment, or white space, as a URL
// parser reads it, so a password that holds an @ is still found whole.
var urlPassword = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://[^/?#\s@:]*):[^/?#\s]+@`)

// stripURLPassword replaces the password of each URL in a value with
// Changed and keeps its username and host, so a pasted URL never stores a
// credential.
func stripURLPassword(value string) string {
	return urlPassword.ReplaceAllString(value, "${1}:"+Changed+"@")
}

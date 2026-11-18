package services

import (
	"strings"
)

// The json-schema gem resolves ids and references with Addressable::URI,
// and keys the schemas it has seen by URI strings. What follows is the
// part of Addressable the gem reaches, for URI text made of printable
// ASCII characters. Text with any other character is not modelled, and a
// schema whose verdict would depend on it is refused.

// The gem gives a schema with no id a name made from a UUID, and resolves
// relative names against the directory Cloud Controller runs in. Neither
// can be written in a schema, so each stands in for one here, made with a
// character that no modelled URI text holds.
const (
	gemRootPath   = "\x01root"
	gemWorkingDir = "/\x01cwd"
)

// rubyURI holds the components of an Addressable::URI, where a nil
// pointer is Ruby's nil.
type rubyURI struct {
	scheme, user, password, host, port, query, fragment *string
	path                                                string
}

func stringPointer(text string) *string {
	return &text
}

// uriTextModelled reports whether every character of text is printable
// ASCII other than a space.
func uriTextModelled(text string) bool {
	for index := range len(text) {
		if text[index] < '!' || text[index] > '~' {
			return false
		}
	}

	return true
}

// parseURIText is Addressable::URI.parse on text from a schema.
func parseURIText(text string) (*rubyURI, error) {
	if !uriTextModelled(text) {
		return nil, gemFault(faultUnmodelled, "the URI %q holds a character outside printable ASCII", text)
	}

	return parseURIParts(text)
}

// parseGemURIText is Addressable::URI.parse on the text of a URI the gem
// made, which may hold the stand-ins for the root name and the working
// directory.
func parseGemURIText(text string) (*rubyURI, error) {
	if !uriTextModelled(strings.ReplaceAll(text, "\x01", "")) {
		return nil, gemFault(faultUnmodelled, "the URI %q holds a character outside printable ASCII", text)
	}

	return parseURIParts(text)
}

func parseURIParts(text string) (*rubyURI, error) {
	parts := rubyURI{}
	rest := text

	if index := strings.IndexAny(rest, ":/?#"); index > 0 && rest[index] == ':' {
		parts.scheme = stringPointer(rest[:index])
		rest = rest[index+1:]
	}

	var authority *string

	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		end := indexOrEnd(rest, "/?#")
		authority = stringPointer(rest[:end])
		rest = rest[end:]
	}

	end := indexOrEnd(rest, "?#")
	parts.path = rest[:end]
	rest = rest[end:]

	if strings.HasPrefix(rest, "?") {
		rest = rest[1:]
		end = indexOrEnd(rest, "#")
		parts.query = stringPointer(rest[:end])
		rest = rest[end:]
	}

	if strings.HasPrefix(rest, "#") {
		parts.fragment = stringPointer(rest[1:])
	}

	var rawPort *string

	if authority != nil {
		if strings.Contains(*authority, "%") {
			return nil, gemFault(faultUnmodelled, "the URI %q has a percent sign in its authority", text)
		}

		parts.user, parts.password, parts.host, rawPort = splitAuthority(*authority)
	}

	return assembleURI(parts, rawPort, true)
}

func indexOrEnd(text, chars string) int {
	index := strings.IndexAny(text, chars)
	if index < 0 {
		return len(text)
	}

	return index
}

// splitAuthority splits an authority the way Addressable's parse does,
// into a user and password before the last @ that comes before any
// bracket, a host, and a port after a last colon.
func splitAuthority(authority string) (*string, *string, *string, *string) {
	var user, password *string

	host := authority
	prefix := authority[:indexOrEnd(authority, "[]")]

	if at := strings.LastIndexByte(prefix, '@'); at >= 0 {
		userinfo := authority[:at]
		host = authority[at+1:]
		name, secret, hasSecret := strings.Cut(userinfo, ":")
		user = stringPointer(name)

		if hasSecret {
			password = stringPointer(secret)
		}
	}

	if colon := portColon(host); colon >= 0 {
		host = host[:colon]
	}

	var port *string

	if colon := portColon(authority); colon >= 0 && colon+1 < len(authority) {
		port = stringPointer(authority[colon+1:])
	}

	return user, password, stringPointer(host), port
}

// portColon is where /:([^:@\[\]]*?)$/ matches in text, or -1.
func portColon(text string) int {
	colon := strings.LastIndexByte(text, ':')
	if colon < 0 || strings.ContainsAny(text[colon+1:], "@[]") {
		return -1
	}

	return colon
}

// assembleURI builds a URI from parts the way Addressable's setters do,
// then validates it. A rawPort is port text still to be checked, and a
// URI made by Addressable's new is also turned into a string, which
// raises for an ambiguous path, while one made by merge is not.
func assembleURI(parts rubyURI, rawPort *string, asNew bool) (*rubyURI, error) {
	built := rubyURI{scheme: parts.scheme, query: parts.query, fragment: parts.fragment}

	if built.scheme != nil && !validScheme(*built.scheme) {
		return nil, gemFault(faultRaised, "Addressable::URI::InvalidURIError: Invalid scheme format: %q", *built.scheme)
	}

	built.user = parts.user
	built.password = parts.password

	if built.password != nil && built.user == nil {
		built.user = stringPointer("")
	}

	built.host = parts.host

	switch {
	case rawPort != nil:
		port, err := checkedPort(*rawPort)
		if err != nil {
			return nil, err
		}

		built.port = port
	case parts.port != nil:
		built.port = parts.port
	}

	built.setPath(parts.path)

	err := built.validate()
	if err != nil {
		return nil, err
	}

	if asNew {
		_, err = built.String()
		if err != nil {
			return nil, err
		}
	}

	return &built, nil
}

func validScheme(scheme string) bool {
	if scheme == "" || !isASCIILetter(scheme[0]) {
		return false
	}

	for index := range len(scheme) {
		char := scheme[index]
		if !isASCIILetter(char) && !isASCIIDigit(char) && char != '.' && char != '+' && char != '-' {
			return false
		}
	}

	return true
}

func isASCIILetter(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isASCIIDigit(char byte) bool {
	return char >= '0' && char <= '9'
}

// checkedPort is Addressable's port=, which takes digits only and keeps
// them as an Integer, with 0 meaning no port.
func checkedPort(text string) (*string, error) {
	if text == "" {
		return nil, gemFault(faultRaised, "Addressable::URI::InvalidURIError: Invalid port number: %q", text)
	}

	for index := range len(text) {
		if !isASCIIDigit(text[index]) {
			return nil, gemFault(faultRaised, "Addressable::URI::InvalidURIError: Invalid port number: %q", text)
		}
	}

	digits := strings.TrimLeft(text, "0")
	if digits == "" {
		return nil, nil //nolint:nilnil // port 0 is Ruby's nil port, and no error
	}

	return stringPointer(digits), nil
}

// String is Addressable's to_s, which raises for a relative URI whose
// path would read as a scheme.
func (uri *rubyURI) String() (string, error) {
	if uri.scheme == nil && uri.path != "" && uri.path[0] != '/' {
		if index := strings.IndexAny(uri.path, "/:"); index >= 0 && uri.path[index] == ':' {
			return "", gemFault(faultRaised, "Addressable::URI::InvalidURIError: Cannot assemble URI string with ambiguous path")
		}
	}

	var text strings.Builder

	if uri.scheme != nil {
		text.WriteString(*uri.scheme + ":")
	}

	if uri.host != nil {
		text.WriteString("//")

		switch {
		case uri.user != nil && uri.password != nil:
			text.WriteString(*uri.user + ":" + *uri.password + "@")
		case uri.user != nil:
			text.WriteString(*uri.user + "@")
		}

		text.WriteString(*uri.host)

		if uri.port != nil {
			text.WriteString(":" + *uri.port)
		}
	}

	text.WriteString(uri.path)

	if uri.query != nil {
		text.WriteString("?" + *uri.query)
	}

	if uri.fragment != nil {
		text.WriteString("#" + *uri.fragment)
	}

	return text.String(), nil
}

// setPath is Addressable's path=, which adds a leading slash to a path
// when the URI has a host.
func (uri *rubyURI) setPath(path string) {
	if path != "" && path[0] != '/' && uri.host != nil {
		path = "/" + path
	}

	uri.path = path
}

// isIPBasedScheme reports whether Addressable's ip_based_schemes holds
// scheme.
func isIPBasedScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ftp", "tftp", "sftp", "ssh", "svn+ssh", "telnet", "nntp", "gopher", "wais", "ldap", "prospero":
		return true
	default:
		return false
	}
}

// validate is Addressable's validate.
//
//nolint:cyclop // it follows Addressable's validate check for check
func (uri *rubyURI) validate() error {
	invalid := func(reason string) error {
		return gemFault(faultRaised, "Addressable::URI::InvalidURIError: %s", reason)
	}

	if uri.scheme != nil && isIPBasedScheme(strings.ToLower(*uri.scheme)) &&
		(uri.host == nil || *uri.host == "") && uri.path == "" {
		return invalid("Absolute URI missing hierarchical segment")
	}

	if uri.host == nil && (uri.port != nil || uri.user != nil || uri.password != nil) {
		return invalid("Hostname not supplied")
	}

	if uri.path != "" && uri.path[0] != '/' && uri.host != nil {
		return invalid("Cannot have a relative path with an authority set")
	}

	if strings.HasPrefix(uri.path, "//") && uri.host == nil {
		return invalid("Cannot have a path with two leading slashes without an authority set")
	}

	if uri.host != nil {
		host := *uri.host
		if strings.ContainsAny(host, "<>{}/\\?#@\"") {
			return invalid("Invalid character in host")
		}

		if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' &&
			strings.ContainsAny(host[1:len(host)-1], "\"#%/<>?@[\\]^`{|}") {
			return invalid("Invalid character in host")
		}
	}

	return nil
}

// dup is Addressable's dup, which builds the URI again with new.
func (uri *rubyURI) dup() (*rubyURI, error) {
	return assembleURI(*uri, nil, true)
}

// withFragment is Addressable's merge(fragment: fragment).
func (uri *rubyURI) withFragment(fragment string) (*rubyURI, error) {
	parts := *uri
	parts.fragment = stringPointer(fragment)

	return assembleURI(parts, nil, false)
}

// stripFragment is the gem's strip_fragment, which empties a fragment
// that has text.
func (uri *rubyURI) stripFragment() (*rubyURI, error) {
	parsed, err := uri.dup()
	if err != nil {
		return nil, err
	}

	if parsed.fragment == nil || *parsed.fragment == "" {
		return parsed, nil
	}

	return parsed.withFragment("")
}

// join is Addressable's join.
func (uri *rubyURI) join(other *rubyURI) (*rubyURI, error) {
	text, err := other.String()
	if err != nil {
		return nil, err
	}

	if text == "" {
		return uri.dup()
	}

	joined := rubyURI{}

	switch {
	case other.scheme != nil:
		joined = rubyURI{scheme: other.scheme, user: other.user, password: other.password, host: other.host,
			port: other.port, path: normalizeURIPath(other.path), query: other.query}
	case other.host != nil:
		joined = rubyURI{user: other.user, password: other.password, host: other.host,
			port: other.port, path: normalizeURIPath(other.path), query: other.query}
	default:
		switch {
		case other.path == "":
			joined.path = uri.path
			joined.query = uri.query

			if other.query != nil {
				joined.query = other.query
			}
		case other.path[0] == '/':
			joined.path = normalizeURIPath(other.path)
			joined.query = other.query
		default:
			base := normalizeURIPath(uri.path)
			if slash := strings.LastIndexByte(base, '/'); slash >= 0 && slash < len(base)-1 {
				base = base[:slash+1]
			} else if slash < 0 {
				base = ""
			}

			if base == "" && uri.host != nil {
				base = "/"
			}

			joined.path = normalizeURIPath(base + other.path)
			joined.query = other.query
		}

		joined.user, joined.password, joined.host, joined.port = uri.user, uri.password, uri.host, uri.port
	}

	if other.scheme == nil {
		joined.scheme = uri.scheme
	}

	joined.fragment = other.fragment

	return assembleURI(joined, nil, true)
}

// normalizeURIPath is Addressable's normalize_path, which removes dot
// segments.
//
//nolint:cyclop,gocognit // it follows normalize_path's loops as Addressable writes them
func normalizeURIPath(path string) string {
	if path == "" {
		return ""
	}

	segments := strings.Split(path, "/")
	output := []string{segments[0]}
	last := len(segments) - 1

	for index := 1; index <= last; index++ {
		switch segment := segments[index]; segment {
		case ".":
			if index == last {
				output = append(output, "")
			}
		case "..":
			if len(output) > 1 {
				output = output[:len(output)-1]
				if index == last {
					output = append(output, "")
				}
			} else {
				output = append(output, "..")
			}
		default:
			output = append(output, segment)
		}
	}

	normalized := strings.Join(output, "/")
	start := 0

	if strings.HasPrefix(normalized, "/") {
		if normalized == "/.." {
			return "/"
		}

		if strings.HasPrefix(normalized, "/../") {
			start = len("/..")
		}
	} else {
		for start < len(normalized) && normalized[start] == '.' {
			start++
			if start < len(normalized) && normalized[start] == '.' {
				start++
			}

			if start < len(normalized) && normalized[start] == '/' {
				start++
			}
		}
	}

	return normalized[start:]
}

// gemSchemaKey is the gem's schema_key_for, the key a schema at uri is
// cached under. An absolute URI is its own key. A relative one becomes a
// file URI under the working directory, through steps that depend on
// the files there, so the key is modelled only when those steps can't
// change it: no dot segments, no trailing slash, nothing percent-encoded
// or encoded on the way, and no drive letter or backslash.
func gemSchemaKey(uri *rubyURI) (string, error) {
	parsed, err := uri.dup()
	if err != nil {
		return "", err
	}

	if parsed.scheme != nil {
		if *parsed.scheme == "file" {
			return "", gemFault(faultUnmodelled, "a file URI's key depends on Cloud Controller's files")
		}

		text, err := parsed.String()
		if err != nil {
			return "", err
		}

		if strings.HasSuffix(text, "#") {
			return text, nil
		}

		return text + "#", nil
	}

	var path string

	switch {
	case strings.HasPrefix(parsed.path, "/"):
		path = parsed.path
	case parsed.host != nil:
		return "", gemFault(faultUnmodelled, "a relative URI with an authority and no path")
	default:
		text, err := parsed.String()
		if err != nil {
			return "", err
		}

		path = gemWorkingDir + "/" + text[:indexOrEnd(text, "?#")]
	}

	if strings.HasPrefix(path, "//") || strings.HasSuffix(path, "/") || strings.ContainsAny(path, "\\\"%<>[]^`{|}") ||
		hasDriveLetter(path) || hasDotSegment(path) {
		return "", gemFault(faultUnmodelled, "the key of %q depends on Cloud Controller's files", path)
	}

	return "file://" + path + "#", nil
}

func hasDriveLetter(path string) bool {
	path = strings.TrimPrefix(path, "/")

	return len(path) >= 3 && isASCIILetter(path[0]) && (path[1] == ':' || path[1] == '|') && (path[2] == '/' || path[2] == '\\')
}

func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}

	return false
}

// rubySplit is Ruby's String#split with a one-character separator, which
// drops trailing empty fields.
func rubySplit(text string, separator string) []string {
	fields := strings.Split(text, separator)
	for len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}

	return fields
}

// normalizeRef is the gem's normalize_ref, which resolves a $ref against
// the URI of the schema that holds it and always leaves a fragment.
//
//nolint:cyclop // it follows the gem's normalize_ref branch for branch
func normalizeRef(ref any, base *rubyURI) (*rubyURI, error) {
	text, err := refText(ref)
	if err != nil {
		return nil, err
	}

	resolved, err := parseURIText(text)
	if err != nil {
		return nil, err
	}

	baseURI, err := base.dup()
	if err != nil {
		return nil, err
	}

	if resolved.scheme == nil {
		own, err := resolved.String()
		if err != nil {
			return nil, err
		}

		fields := rubySplit(own, "#")
		merged := *baseURI
		resolved = &merged

		switch {
		case len(fields) == 0 || fields[0] == "":
			resolved.setPath(baseURI.path)
		case fields[0][0] == '/':
			if fields[0] != "/" && (strings.Contains(fields[0], "//") || strings.HasSuffix(fields[0], "/") || hasDotSegment(fields[0])) {
				return nil, gemFault(faultUnmodelled, "the reference %q needs Pathname#cleanpath", text)
			}

			resolved.setPath(fields[0])
		default:
			relative, err := parseURIText(fields[0])
			if err != nil {
				return nil, err
			}

			resolved, err = resolved.join(relative)
			if err != nil {
				return nil, err
			}
		}

		resolved.fragment = nil
		if len(fields) > 1 {
			resolved.fragment = stringPointer(fields[1])
		}
	}

	if resolved.fragment == nil {
		resolved.fragment = stringPointer("")
	}

	err = resolved.validate()
	if err != nil {
		return nil, err
	}

	return resolved, nil
}

// refText is a reference as the gem's URI parse takes it. nil and false
// parse to nil, which has no normalize_ref, and any value other than a
// string is a TypeError.
func refText(ref any) (string, error) {
	switch typed := ref.(type) {
	case string:
		return typed, nil
	case nil, bool:
		return "", gemFault(faultRaised, "NoMethodError: a reference of %v", typed)
	default:
		return "", gemFault(faultRaised, "TypeError: a reference that is not a string")
	}
}

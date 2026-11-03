package yamlnode

// directiveRun checks the directive lines of one run, the directives
// that come before the "---" of one document, as libyaml scans and
// processes them.
type directiveRun struct {
	lines   []int           // the line of each directive, counted from 0
	version bool            // a %YAML directive came
	handles map[string]bool // the %TAG handles declared so far
}

// directive is what scanDirective reads from a directive line.
type directive struct {
	name         string // "YAML" or "TAG"
	major, minor int    // the version a %YAML directive names
	handle       string // the handle a %TAG directive declares
}

// check reads line, the directive on line n counted from 0, and returns
// libyaml's error for it, or nil. libyaml scans a directive line before
// it checks the directive against the ones before it, and it checks
// each one before it scans the next, so the first fault in line order
// is the one it reports, on the directive's line.
func (r *directiveRun) check(line []byte, n int) error {
	d, msg := scanDirective(line)
	if msg == "" {
		msg = r.declare(d)
	}
	if msg != "" {
		return &ParseError{Line: n, SourceLine: n + 1, Message: msg}
	}
	r.lines = append(r.lines, n)
	return nil
}

// declare records d and returns libyaml's error when the run already
// holds a %YAML directive or d's handle, or when d names a version
// other than 1.1, compared as numbers.
func (r *directiveRun) declare(d directive) string {
	if d.name == "YAML" {
		switch {
		case r.version:
			return "found duplicate %YAML directive"
		case d.major != 1 || d.minor != 1:
			return "found incompatible YAML document"
		}
		r.version = true
		return ""
	}
	if r.handles[d.handle] {
		return "found duplicate %TAG directive"
	}
	if r.handles == nil {
		r.handles = map[string]bool{}
	}
	r.handles[d.handle] = true
	return ""
}

// scanDirective reads line, a line that starts with "%" and holds no
// line break, as libyaml's scanner reads a directive. It returns the
// directive, or the scanner's error for the line.
//
// The name is a run of letters, digits, "_", and "-" that a blank or
// the end of the line follows. A %YAML directive names a version of two
// numbers of one or two digits each, joined by a ".". A %TAG directive
// names a handle, which is "!", "!!", or "!" and a name and "!", and
// then a prefix of URI characters, which may be empty. Blanks come
// between the parts, and after the last part only blanks, then a
// comment, may follow. A "#" starts a comment there even with no blank
// before it.
func scanDirective(line []byte) (directive, string) {
	s := &directiveScan{propertyScan{text: line, i: 1}}
	d := directive{name: string(s.alpha())}
	switch {
	case d.name == "":
		return d, "could not find expected directive name"
	case !s.atBlankOrEnd():
		return d, "found unexpected non-alphabetical character"
	}
	var msg string
	switch d.name {
	case "YAML":
		msg = s.version(&d)
	case "TAG":
		msg = s.tag(&d)
	default:
		return d, "found unknown directive name"
	}
	if msg != "" {
		return d, msg
	}
	s.skipBlanks()
	if s.peek() != '#' && s.peek() != 0 {
		return d, "did not find expected comment or line break"
	}
	return d, ""
}

// directiveScan reads a directive line.
type directiveScan struct {
	propertyScan
}

// version reads the version of a %YAML directive into d.
func (s *directiveScan) version(d *directive) string {
	s.skipBlanks()
	var msg string
	if d.major, msg = s.versionNumber(); msg != "" {
		return msg
	}
	if s.peek() != '.' {
		return "did not find expected digit or '.' character"
	}
	s.i++
	d.minor, msg = s.versionNumber()
	return msg
}

// versionNumber reads one number of a version.
func (s *directiveScan) versionNumber() (int, string) {
	value, length := 0, 0
	for c := s.peek(); c >= '0' && c <= '9'; c = s.peek() {
		length++
		if length > 2 {
			return 0, "found extremely long version number"
		}
		value = value*10 + int(c-'0')
		s.i++
	}
	if length == 0 {
		return 0, "did not find expected version number"
	}
	return value, ""
}

// tag reads the handle and the prefix of a %TAG directive, and keeps
// the handle in d.
func (s *directiveScan) tag(d *directive) string {
	s.skipBlanks()
	start := s.i
	if s.peek() != '!' {
		return uriOctetMessage
	}
	s.i++
	name := s.alpha()
	if s.peek() == '!' {
		s.i++
	} else if len(name) > 0 {
		return uriOctetMessage
	}
	d.handle = string(s.text[start:s.i])
	if c := s.peek(); c != ' ' && c != '\t' {
		return "did not find expected whitespace"
	}
	s.skipBlanks()
	if s.uri() != "" {
		return uriOctetMessage
	}
	if !s.atBlankOrEnd() {
		return "did not find expected whitespace or line break"
	}
	return ""
}

// skipBlanks moves past spaces and tabs.
func (s *directiveScan) skipBlanks() {
	for s.peek() == ' ' || s.peek() == '\t' {
		s.i++
	}
}

// atBlankOrEnd reports whether a space, a tab, or the end of the line
// comes next.
func (s *directiveScan) atBlankOrEnd() bool {
	c := s.peek()
	return c == ' ' || c == '\t' || c == 0
}

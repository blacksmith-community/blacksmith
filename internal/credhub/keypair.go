package credhub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	opKeyPair = "key pair read"
	// maxKeyPairBody bounds a key pair answer.
	maxKeyPairBody = 64 << 10
	// maxKeyPairValue bounds the decoded certificate and the decoded key.
	maxKeyPairValue = 16 << 10
	// maxKeyPairDepth bounds how deeply the answer may nest.
	maxKeyPairDepth = 64
	// maxMemberName is the longest member name the decoder compares. A
	// longer name matches nothing and is skipped.
	maxMemberName   = 32
	redactedKeyPair = "KeyPair{<redacted>}"
	// firstPrintable is the first byte JSON allows raw in a string.
	firstPrintable = 0x20
	// asciiEnd is the first byte past ASCII.
	asciiEnd = 0x80
	// hexBits is the number of bits one hex digit carries.
	hexBits = 4
	// hexLetter is the value of the hex digit a.
	hexLetter = 10
)

// ErrBadKeyPairAnswer is the error every key pair decoding failure wraps.
var ErrBadKeyPairAnswer = errors.New("CredHub key pair answer could not be decoded")

// ErrKeyPairNotSerialisable is what MarshalJSON returns, so a key pair can
// never be written into a record or an API response by mistake.
var ErrKeyPairNotSerialisable = errors.New("a key pair is never serialised")

// KeyPair is one certificate version with its private key, as PEM bytes.
// Only patch mode reads one, and it keeps the pair only as long as one
// VM's staging step needs it. Formatting a KeyPair in any way shows
// KeyPair{<redacted>}, and it cannot be marshalled to JSON.
type KeyPair struct { //nolint:recvcheck // value receivers redact values too, and Zero needs a pointer
	VersionID   string
	Certificate []byte
	PrivateKey  []byte
}

// Zero overwrites the certificate and the private key with zeros. It is
// a best effort that keeps the key's lifetime short. Go may already have
// copied the bytes elsewhere, so it does not promise that no copy is
// left. A nil KeyPair is ignored.
func (k *KeyPair) Zero() {
	if k == nil {
		return
	}

	clear(k.Certificate)
	clear(k.PrivateKey)
}

// The formatting methods have value receivers, so a KeyPair value is
// redacted as well as a pointer to one. Zero needs a pointer.

// String returns KeyPair{<redacted>}.
func (KeyPair) String() string { return redactedKeyPair }

// GoString returns KeyPair{<redacted>}, so %#v shows no content either.
func (KeyPair) GoString() string { return redactedKeyPair }

// Format writes KeyPair{<redacted>} for every verb, so no verb, such as
// %x or %d, can print the bytes.
func (KeyPair) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redactedKeyPair)
}

// MarshalJSON always fails, because a key pair never goes into a record.
func (KeyPair) MarshalJSON() ([]byte, error) {
	return nil, ErrKeyPairNotSerialisable
}

// CertificateKeyPair returns the certificate and the private key of one
// certificate version. It is the only call that reads a private key, and
// patch mode is its only caller. A failed answer carries only its status,
// an answer larger than 64 KiB fails with ErrAnswerTooLarge, and a body
// that cannot be decoded fails with an error that wraps
// ErrBadKeyPairAnswer and names only the failure and its offset. The
// answer buffer is cleared before the call returns, on every path. The
// caller calls Zero on the pair once it is done with it.
func (c *Client) CertificateKeyPair(ctx context.Context, versionID string) (*KeyPair, error) {
	if versionID == "" {
		return nil, ErrEmptyVersionID
	}

	body, err := c.send(ctx, request{
		operation:    opKeyPair,
		method:       http.MethodGet,
		path:         dataPath + "/" + url.PathEscape(versionID),
		maxBody:      maxKeyPairBody,
		opaqueErrors: true,
		oneBuffer:    true,
	})

	defer func() {
		clear(body)

		if c.bufferSeen != nil {
			c.bufferSeen(body)
		}
	}()

	if err != nil {
		return nil, notFoundOn404(err, versionID)
	}

	cert, key, err := decodeKeyPair(body)
	if err != nil {
		return nil, fmt.Errorf("CredHub %s: %w", opKeyPair, err)
	}

	return &KeyPair{VersionID: versionID, Certificate: cert, PrivateKey: key}, nil
}

// keyPairError is a decoding failure. It holds a fixed phrase and an
// offset, never any input.
type keyPairError struct {
	phrase string
	offset int
}

func (e *keyPairError) Error() string {
	return fmt.Sprintf("key pair answer %s at offset %d", e.phrase, e.offset)
}

func (e *keyPairError) Is(target error) bool { return target == ErrBadKeyPairAnswer }

// The fixed phrases a decoding failure uses.
const (
	phraseNotJSON    = "is not valid JSON"
	phraseNotObject  = "is not a JSON object"
	phraseOpenString = "ends inside a string"
	phraseEscape     = "has an escape that is not allowed"
	phraseControl    = "has a control character in a string"
	phraseTooLong    = "has a value longer than 16384 bytes"
	phraseTooDeep    = "nests deeper than 64 levels"
	phraseTrailing   = "has data after the object"
	phraseValueKind  = "has a value member that is not an object"
	phraseNoValue    = "has no value member"
	phraseRepeatCert = "repeats certificate"
	phraseRepeatKey  = "repeats private_key"
	phraseCertKind   = "has a certificate that is not a string"
	phraseKeyKind    = "has a private_key that is not a string"
	phraseNoCert     = "has no certificate"
	phraseNoKey      = "has no private_key"
	phraseEmptyCert  = "has an empty certificate"
	phraseEmptyKey   = "has an empty private_key"
)

// The JSON literals the decoder skips.
const (
	literalFalse = "false"
	literalTrue  = "true"
	literalNull  = "null"
)

// memberKind is what a member name means to the decoder.
type memberKind int

const (
	memberOther memberKind = iota
	memberValue
	memberCertificate
	memberPrivateKey
)

// keyPairDecoder walks one CredHub data answer to value.certificate and
// value.private_key. It decodes those two strings straight into byte
// slices and skips every other member without keeping it.
type keyPairDecoder struct {
	body      []byte
	pos       int
	sawValue  bool
	sawCert   bool
	sawKey    bool
	cert, key []byte
}

// decodeKeyPair returns the certificate and the private key in a CredHub
// data answer. encoding/json cannot be used, because it decodes a JSON
// string into a []byte as base64 and into a string as an immutable copy
// of the key. This decoder accepts only the escapes \n, \r, \t, \", \\,
// \/, and \u0000 to \u007f in the two values, rejects a raw control
// character in any string, caps each value at 16 KiB, rejects a repeated
// certificate or private_key, and rejects a body that ends inside a
// string. Its errors name the failure and its offset and never quote the
// body. On failure it clears whatever it decoded and returns no bytes.
func decodeKeyPair(body []byte) ([]byte, []byte, error) {
	dec := &keyPairDecoder{body: body}

	err := dec.document()
	if err != nil {
		clear(dec.cert)
		clear(dec.key)

		return nil, nil, err
	}

	return dec.cert, dec.key, nil
}

func (d *keyPairDecoder) fail(phrase string) error {
	return &keyPairError{phrase: phrase, offset: d.pos}
}

// document reads the whole answer, which must be one object with a value
// member that holds both halves, and nothing after it but white space.
func (d *keyPairDecoder) document() error {
	d.space()

	if !d.at('{') {
		return d.fail(phraseNotObject)
	}

	err := d.object(1, d.topMember)
	if err != nil {
		return err
	}

	d.space()

	if d.pos != len(d.body) {
		return d.fail(phraseTrailing)
	}

	switch {
	case !d.sawValue:
		return d.fail(phraseNoValue)
	case !d.sawCert:
		return d.fail(phraseNoCert)
	case !d.sawKey:
		return d.fail(phraseNoKey)
	case len(d.cert) == 0:
		return d.fail(phraseEmptyCert)
	case len(d.key) == 0:
		return d.fail(phraseEmptyKey)
	}

	return nil
}

// topMember handles one member of the answer object.
func (d *keyPairDecoder) topMember(kind memberKind, depth int) error {
	if kind != memberValue {
		return d.skip(depth)
	}

	if !d.at('{') {
		return d.fail(phraseValueKind)
	}

	d.sawValue = true

	return d.object(depth+1, d.valueMember)
}

// valueMember handles one member of the value object.
func (d *keyPairDecoder) valueMember(kind memberKind, depth int) error {
	switch kind {
	case memberCertificate:
		if d.sawCert {
			return d.fail(phraseRepeatCert)
		}

		if !d.at('"') {
			return d.fail(phraseCertKind)
		}

		d.sawCert = true

		return d.value(&d.cert)
	case memberPrivateKey:
		if d.sawKey {
			return d.fail(phraseRepeatKey)
		}

		if !d.at('"') {
			return d.fail(phraseKeyKind)
		}

		d.sawKey = true

		return d.value(&d.key)
	case memberOther, memberValue:
		return d.skip(depth)
	}

	return d.skip(depth)
}

// value decodes the string at the cursor into a new slice whose capacity
// already covers the longest value it may hold, so appending never moves
// the bytes and leaves no partial copy behind.
func (d *keyPairDecoder) value(dst *[]byte) error {
	*dst = make([]byte, 0, min(len(d.body)-d.pos, maxKeyPairValue))

	overflow, err := d.str(dst, maxKeyPairValue)
	if err != nil {
		return err
	}

	if overflow {
		return d.fail(phraseTooLong)
	}

	return nil
}

// object reads the object at the cursor and calls member for each member,
// with the cursor at the member's value. depth is the object's own depth.
func (d *keyPairDecoder) object(depth int, member func(memberKind, int) error) error {
	if depth > maxKeyPairDepth {
		return d.fail(phraseTooDeep)
	}

	d.pos++
	d.space()

	if d.at('}') {
		d.pos++

		return nil
	}

	for {
		d.space()

		if !d.at('"') {
			return d.fail(phraseNotJSON)
		}

		kind, err := d.name()
		if err != nil {
			return err
		}

		d.space()

		if !d.at(':') {
			return d.fail(phraseNotJSON)
		}

		d.pos++
		d.space()

		err = member(kind, depth)
		if err != nil {
			return err
		}

		d.space()

		switch {
		case d.at(','):
			d.pos++
		case d.at('}'):
			d.pos++

			return nil
		default:
			return d.fail(phraseNotJSON)
		}
	}
}

// name reads a member name and says which member it is. A name longer
// than any the decoder looks for matches nothing.
func (d *keyPairDecoder) name() (memberKind, error) {
	var buf [maxMemberName]byte

	decoded := buf[:0]

	overflow, err := d.str(&decoded, maxMemberName)
	if err != nil || overflow {
		return memberOther, err
	}

	switch string(decoded) {
	case "value":
		return memberValue, nil
	case "certificate":
		return memberCertificate, nil
	case "private_key":
		return memberPrivateKey, nil
	default:
		return memberOther, nil
	}
}

// skip reads past the value at the cursor without keeping any of it.
// depth is the depth of the object or array that holds the value.
func (d *keyPairDecoder) skip(depth int) error {
	if d.pos >= len(d.body) {
		return d.fail(phraseNotJSON)
	}

	switch d.body[d.pos] {
	case '{':
		return d.object(depth+1, func(_ memberKind, inner int) error { return d.skip(inner) })
	case '[':
		return d.array(depth + 1)
	case '"':
		_, err := d.str(nil, 0)

		return err
	case 't':
		return d.literal(literalTrue)
	case 'f':
		return d.literal(literalFalse)
	case 'n':
		return d.literal(literalNull)
	default:
		return d.number()
	}
}

// array reads past the array at the cursor.
func (d *keyPairDecoder) array(depth int) error {
	if depth > maxKeyPairDepth {
		return d.fail(phraseTooDeep)
	}

	d.pos++
	d.space()

	if d.at(']') {
		d.pos++

		return nil
	}

	for {
		d.space()

		err := d.skip(depth)
		if err != nil {
			return err
		}

		d.space()

		switch {
		case d.at(','):
			d.pos++
		case d.at(']'):
			d.pos++

			return nil
		default:
			return d.fail(phraseNotJSON)
		}
	}
}

// literal reads past true, false, or null.
func (d *keyPairDecoder) literal(word string) error {
	end := d.pos + len(word)
	if end > len(d.body) || string(d.body[d.pos:end]) != word {
		return d.fail(phraseNotJSON)
	}

	d.pos = end

	return nil
}

// number reads past a JSON number.
func (d *keyPairDecoder) number() error {
	if d.at('-') {
		d.pos++
	}

	switch {
	case d.at('0'):
		d.pos++
	case d.digits() == 0:
		return d.fail(phraseNotJSON)
	}

	if d.at('.') {
		d.pos++

		if d.digits() == 0 {
			return d.fail(phraseNotJSON)
		}
	}

	if d.at('e') || d.at('E') {
		d.pos++

		if d.at('+') || d.at('-') {
			d.pos++
		}

		if d.digits() == 0 {
			return d.fail(phraseNotJSON)
		}
	}

	return nil
}

// digits reads past a run of decimal digits and returns its length.
func (d *keyPairDecoder) digits() int {
	start := d.pos

	for d.pos < len(d.body) && d.body[d.pos] >= '0' && d.body[d.pos] <= '9' {
		d.pos++
	}

	return d.pos - start
}

// str reads the string at the cursor and leaves the cursor after its
// closing quote. With dst set it appends the decoded bytes to *dst, up to
// limit bytes, accepting only the escapes \n, \r, \t, \", \\, \/, and
// \u0000 to \u007f, and reports whether the text was longer than limit.
// With dst nil it keeps nothing and accepts every JSON escape. Either way
// a raw control character, a malformed escape, or the end of the body
// inside the string fails.
func (d *keyPairDecoder) str(dst *[]byte, limit int) (bool, error) {
	overflow := false

	d.pos++

	for {
		if d.pos >= len(d.body) {
			return false, d.fail(phraseOpenString)
		}

		next := d.body[d.pos]

		switch {
		case next == '"':
			d.pos++

			return overflow, nil
		case next < firstPrintable:
			return false, d.fail(phraseControl)
		case next == '\\':
			decoded, err := d.escape(dst != nil)
			if err != nil {
				return false, err
			}

			next = decoded
		default:
			d.pos++
		}

		if dst == nil {
			continue
		}

		if len(*dst) >= limit {
			overflow = true

			continue
		}

		*dst = append(*dst, next)
	}
}

// escape reads the escape at the cursor and returns the byte it stands
// for. Strict decoding accepts only the escapes a PEM value needs, and a
// \u escape only below 0x80. Otherwise every JSON escape is accepted, and
// the byte returned is not used.
func (d *keyPairDecoder) escape(strict bool) (byte, error) {
	if d.pos+1 >= len(d.body) {
		return 0, d.fail(phraseOpenString)
	}

	kind := d.body[d.pos+1]

	switch kind {
	case 'n':
		d.pos += 2

		return '\n', nil
	case 'r':
		d.pos += 2

		return '\r', nil
	case 't':
		d.pos += 2

		return '\t', nil
	case '"', '\\', '/':
		d.pos += 2

		return kind, nil
	case 'b', 'f':
		if strict {
			return 0, d.fail(phraseEscape)
		}

		d.pos += 2

		return 0, nil
	case 'u':
		return d.unicode(strict)
	default:
		return 0, d.fail(phraseEscape)
	}
}

// unicode reads a \uXXXX escape at the cursor.
func (d *keyPairDecoder) unicode(strict bool) (byte, error) {
	const width = 6

	if d.pos+width > len(d.body) {
		return 0, d.fail(phraseOpenString)
	}

	value := 0

	for _, raw := range d.body[d.pos+2 : d.pos+width] {
		digit, ok := hexDigit(raw)
		if !ok {
			return 0, d.fail(phraseEscape)
		}

		value = value<<hexBits | digit
	}

	if strict && value >= asciiEnd {
		return 0, d.fail(phraseEscape)
	}

	d.pos += width

	return byte(value % asciiEnd), nil
}

func hexDigit(digit byte) (int, bool) {
	switch {
	case digit >= '0' && digit <= '9':
		return int(digit - '0'), true
	case digit >= 'a' && digit <= 'f':
		return int(digit-'a') + hexLetter, true
	case digit >= 'A' && digit <= 'F':
		return int(digit-'A') + hexLetter, true
	default:
		return 0, false
	}
}

// at reports whether the byte at the cursor is c.
func (d *keyPairDecoder) at(c byte) bool {
	return d.pos < len(d.body) && d.body[d.pos] == c
}

// space reads past JSON white space.
func (d *keyPairDecoder) space() {
	for d.pos < len(d.body) {
		switch d.body[d.pos] {
		case ' ', '\t', '\n', '\r':
			d.pos++
		default:
			return
		}
	}
}

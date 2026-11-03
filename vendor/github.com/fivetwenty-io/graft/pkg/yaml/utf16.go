package yaml

import (
	"encoding/binary"
	"unicode/utf16"
)

// utf16Order returns the byte order of data when it starts with a UTF-16
// byte order mark, and nil otherwise.
func utf16Order(data []byte) binary.ByteOrder {
	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return binary.LittleEndian
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return binary.BigEndian
	}
	return nil
}

// pairedSurrogates reports whether every surrogate in units sits in a
// high and low pair.
func pairedSurrogates(units []uint16) bool {
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xD800 && u < 0xDC00 && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] < 0xE000:
			i++
		case u >= 0xD800 && u < 0xE000:
			return false
		}
	}
	return true
}

// toUTF8 returns data as UTF-8 when it starts with a UTF-16 byte order
// mark, which graft's reader would otherwise take for text, and returns
// data itself otherwise. A UTF-16 document with no content, with an odd
// number of bytes, or with an unpaired surrogate is a syntax error. The
// errors say nothing about the contents.
func toUTF8(data []byte) ([]byte, error) {
	order := utf16Order(data)
	if order == nil {
		return data, nil
	}
	body := data[2:]
	switch {
	case len(body) == 0:
		return nil, &DecodeError{Kind: ErrorKindSyntax, Msg: "a UTF-16 document has no content after its byte order mark"}
	case len(body)%2 != 0:
		return nil, &DecodeError{Kind: ErrorKindSyntax, Msg: "a UTF-16 document has an odd number of bytes"}
	}
	units := make([]uint16, len(body)/2)
	for i := range units {
		units[i] = order.Uint16(body[2*i:])
	}
	if !pairedSurrogates(units) {
		return nil, &DecodeError{Kind: ErrorKindSyntax, Msg: "a UTF-16 document holds an unpaired surrogate"}
	}
	return []byte(string(utf16.Decode(units))), nil
}

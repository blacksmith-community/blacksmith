// Copyright (c) 2011-2019 Canonical Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Ported from go.yaml.in/yaml/v3 v3.0.4 resolve.go and modified for
// graft: only the tag decision is kept, and it returns short tags.

package yamlnode

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

const longTagPrefix = "tag:yaml.org,2002:"

// The short tags yaml.v3 assigns. Every package in the module that looks at
// a node's tag uses these names rather than repeating the literals.
const (
	// TagNull is the short tag of a null scalar.
	TagNull = "!!null"
	// TagBool is the short tag of a boolean scalar.
	TagBool = "!!bool"
	// TagInt is the short tag of an integer scalar.
	TagInt = "!!int"
	// TagFloat is the short tag of a floating point scalar.
	TagFloat = "!!float"
	// TagStr is the short tag of a string scalar.
	TagStr = "!!str"
	// TagTimestamp is the short tag of a timestamp scalar.
	TagTimestamp = "!!timestamp"
	// TagMerge is the short tag of the "<<" merge key.
	TagMerge = "!!merge"
	// TagBinary is the short tag of a base64 binary scalar.
	TagBinary = "!!binary"
	// TagMap is the short tag of a mapping.
	TagMap = "!!map"
	// TagSeq is the short tag of a sequence.
	TagSeq = "!!seq"
)

// ResolvePlainTag returns the tag yaml.v3 gives an untagged plain scalar.
// Its boolean literals are only the YAML 1.2 core forms (true, True, TRUE,
// false, False, and FALSE). That differs on purpose from the YAML 1.1 set
// that toBool in internal/yamldiff accepts for nodes already tagged !!bool,
// and from the yes, no, on, and off table in pkg/graft that serves merge,
// so the three tables must not be merged.
func ResolvePlainTag(in string) string {
	if in == "<<" {
		return TagMerge
	}
	switch in {
	case "", "~", "null", "Null", "NULL":
		return TagNull
	case "true", "True", "TRUE", "false", "False", "FALSE":
		return TagBool
	case ".nan", ".NaN", ".NAN", ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF", "-.inf", "-.Inf", "-.INF":
		return TagFloat
	}
	switch c := in[0]; {
	case c == '.':
		if _, err := strconv.ParseFloat(in, 64); err == nil {
			return TagFloat
		}
	case c == '+' || c == '-' || (c >= '0' && c <= '9'):
		return resolveNumeric(in)
	}
	return TagStr
}

func resolveNumeric(in string) string {
	if isTimestamp(in) {
		return TagTimestamp
	}
	plain := strings.ReplaceAll(in, "_", "")
	if _, err := strconv.ParseInt(plain, 0, 64); err == nil {
		return TagInt
	}
	if _, err := strconv.ParseUint(plain, 0, 64); err == nil {
		return TagInt
	}
	if yamlStyleFloat.MatchString(plain) {
		if _, err := strconv.ParseFloat(plain, 64); err == nil {
			return TagFloat
		}
	}
	switch {
	case strings.HasPrefix(plain, "0b"):
		if fitsBase(plain[2:], 2) {
			return TagInt
		}
	case strings.HasPrefix(plain, "-0b"):
		if _, err := strconv.ParseInt("-"+plain[3:], 2, 64); err == nil {
			return TagInt
		}
	case strings.HasPrefix(plain, "0o"):
		if fitsBase(plain[2:], 8) {
			return TagInt
		}
	case strings.HasPrefix(plain, "-0o"):
		if _, err := strconv.ParseInt("-"+plain[3:], 8, 64); err == nil {
			return TagInt
		}
	}
	return TagStr
}

func fitsBase(s string, base int) bool {
	if _, err := strconv.ParseInt(s, base, 64); err == nil {
		return true
	}
	_, err := strconv.ParseUint(s, base, 64)
	return err == nil
}

var yamlStyleFloat = regexp.MustCompile(`^[-+]?(\.\d+|\d+(\.\d*)?)([eE][-+]?\d+)?$`)

var allowedTimestampFormats = []string{
	"2006-1-2T15:4:5.999999999Z07:00",
	"2006-1-2t15:4:5.999999999Z07:00",
	"2006-1-2 15:4:5.999999999",
	"2006-1-2",
}

func isTimestamp(s string) bool {
	_, ok := parseTimestamp(s)
	return ok
}

func parseTimestamp(s string) (time.Time, bool) {
	i := 0
	for ; i < len(s); i++ {
		if c := s[i]; c < '0' || c > '9' {
			break
		}
	}
	if i != 4 || i == len(s) || s[i] != '-' {
		return time.Time{}, false
	}
	for _, format := range allowedTimestampFormats {
		if t, err := time.Parse(format, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ShortTag turns a long yaml.org tag into its "!!" form and returns any
// other tag unchanged.
func ShortTag(tag string) string {
	if strings.HasPrefix(tag, longTagPrefix) {
		return "!!" + tag[len(longTagPrefix):]
	}
	return tag
}

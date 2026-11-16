package yamlprep

import (
	"strings"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
)

// Tokenize returns the tokens goccy's lexer gives src, with two of the
// scanner's misreadings of a tab put right, at the positions goccy gives
// each token when it reads the text right. A double-quoted scalar that
// holds a tab between two characters reads as libyaml reads it, and so
// does a tag or an anchor with a tab after it. Both fixes start from the
// tokens of a copy of src with each tab made a space, which goccy reads
// right and which leaves every byte where it is.
//
// A tab after a tag or an anchor's name becomes a space, which YAML reads
// the same way there, and goes back into the source text of the tokens
// afterwards. A tab inside a double-quoted scalar becomes a character
// that src does not hold, which the scanner counts once and keeps in the
// value as it keeps the tab, and goes back into the value and the source
// text of the scalar afterwards. A tab that starts a continuation line,
// or ends one, stays as it is, since goccy reads those right. When the
// marker reaches any token other than a double-quoted scalar, the quoted
// tabs stay as they are.
//
// A plain << key with a tab before or after its colon is a merge key, as
// it is with a space there, as mergeKeysBeforeTabs describes, and a <<
// key tagged !!merge or !!str reads as YAML 1.1 reads it, as
// taggedMergeKeys describes.
func Tokenize(src string) token.Tokens {
	toks := tokenize(src)
	mergeKeysBeforeTabs(src, toks)
	taggedMergeKeys(toks)
	return toks
}

// tokenize is Tokenize without the fix for merge keys.
func tokenize(src string) token.Tokens {
	quoted, property := mayHoldQuotedTab(src), mayHoldPropertyTab(src)
	if !quoted && !property {
		return lexer.Tokenize(src)
	}
	neutral := strings.ReplaceAll(src, "\t", " ")
	scan := lexer.Tokenize(neutral)
	idx := newByteIndex(neutral)
	var separators []propertyTab
	if property {
		separators = propertyTabs(src, scan, idx)
	}
	text := spaceAt(src, separators)
	var toks token.Tokens
	if quoted {
		if tabs := doubledQuotedTabs(text, scan, idx); len(tabs) > 0 {
			toks, _ = tokenizeQuotedTabs(text, tabs)
		}
	}
	if toks == nil {
		toks = lexer.Tokenize(text)
	}
	keepPropertyTabs(toks, scan, text, separators)
	return toks
}

// mergeKeysBeforeTabs makes each plain << key in toks, the tokens of
// src, a merge key when a tab sits between the << and its colon or right
// after the colon. goccy's scanner reads << as a merge key only when
// nothing but spaces comes before the colon and a space or a line break
// comes after it, so with a tab there the key reads as the string <<,
// where YAML, and spruce, read a merge key. The keys to change are the
// ones the scanner reads as merge keys in a copy of src with each tab
// made a space, which leaves every token where it is.
func mergeKeysBeforeTabs(src string, toks token.Tokens) {
	if !mayHoldTabbedMergeKey(src) {
		return
	}
	merges := make(map[[2]int]bool)
	for _, tk := range lexer.Tokenize(strings.ReplaceAll(src, "\t", " ")) {
		if tk.Type == token.MergeKeyType {
			merges[[2]int{tk.Position.Line, tk.Position.Column}] = true
		}
	}
	for _, tk := range toks {
		if tk.Type == token.StringType && tk.Value == "<<" && merges[[2]int{tk.Position.Line, tk.Position.Column}] {
			merge := token.MergeKey(tk.Origin, tk.Position)
			tk.Type, tk.CharacterType, tk.Indicator = merge.Type, merge.CharacterType, merge.Indicator
		}
	}
}

// mayHoldTabbedMergeKey reports whether src holds << followed by spaces
// or tabs, a colon, and a tab, or << followed by a tab and then a colon
// after any spaces or tabs, which is the shape mergeKeysBeforeTabs
// changes.
func mayHoldTabbedMergeKey(src string) bool {
	for i := strings.Index(src, "<<"); i >= 0; {
		j := i + 2
		tab := false
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			tab = tab || src[j] == '\t'
			j++
		}
		if j < len(src) && src[j] == ':' && (tab || j+1 < len(src) && src[j+1] == '\t') {
			return true
		}
		next := strings.Index(src[i+2:], "<<")
		if next < 0 {
			return false
		}
		i += 2 + next
	}
	return false
}

// taggedMergeKeys makes the << of "? !!merge <<" a merge key and the <<
// of "!!str <<" a plain string, as YAML 1.1 reads them. Both tags stay
// in toks, and no token moves.
//
// goccy reads the << of "? !!merge <<" as a string, so the tag finds no
// merge key to go with and the parse fails, and it reads the << of
// "!!str <<:" as a merge key, which a !!str tag cannot hold. A << counts
// when it is plain and sits right after its tag on the tag's line. A
// !!merge tag counts only right after a "?", since goccy already reads
// "!!merge <<:" in a block or flow mapping as a merge key, and a !!str
// tag counts there too or when its << ends at a colon. Any other tag on
// << stays as goccy reads it.
func taggedMergeKeys(toks token.Tokens) {
	for i := range toks {
		if !isTaggedMergeKey(toks, i) {
			continue
		}
		key := toks[i+1]
		if toks[i].Value == "!!str" {
			key.Type, key.CharacterType, key.Indicator = token.StringType, token.CharacterTypeMiscellaneous, token.NotIndicator
			continue
		}
		merge := token.MergeKey(key.Origin, key.Position)
		key.Type, key.CharacterType, key.Indicator = merge.Type, merge.CharacterType, merge.Indicator
	}
}

// isTaggedMergeKey reports whether toks[i] is a !!merge or !!str tag
// whose << taggedMergeKeys changes, as it describes.
func isTaggedMergeKey(toks token.Tokens, i int) bool {
	tag := toks[i]
	if tag.Type != token.TagType || i+1 >= len(toks) {
		return false
	}
	key := toks[i+1]
	if key.Value != "<<" || (key.Type != token.MergeKeyType && key.Type != token.StringType) ||
		key.Position.Line != tag.Position.Line {
		return false
	}
	explicit := i > 0 && toks[i-1].Type == token.MappingKeyType
	switch tag.Value {
	case "!!merge":
		return explicit
	case "!!str":
		return explicit || (i+2 < len(toks) && toks[i+2].Type == token.MappingValueType)
	}
	return false
}

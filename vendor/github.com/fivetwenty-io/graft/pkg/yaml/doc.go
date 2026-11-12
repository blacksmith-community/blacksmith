/*
Package yaml reads and writes YAML the way graft's merge engine does, so a
program that decodes configuration with this package sees the same values
that graft merge sees.

The package decodes with Unmarshal and UnmarshalWith, encodes with Marshal
and MarshalWith, and exposes a parsed document as a Node tree. It is built
on the decoder and encoder that graft uses internally. A document therefore
reads the same here as it does in a merge, and a value written here reads
back the same in both.

# Quick start

Decode a document into a struct, a map, or a slice, and look at the error
with errors.As when the document is wrong.

	type Config struct {
		Name    string   `yaml:"name"`
		Workers int      `yaml:"workers"`
		Hosts   []string `yaml:"hosts,omitempty"`
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		var de *yaml.DecodeError
		if errors.As(err, &de) {
			log.Printf("line %d: %s", de.Line, de.Msg)
		}
		return err
	}

	out, err := yaml.Marshal(cfg)

Decode into a *any, a *map[string]any, or a *[]any when the shape is not
known in advance. Maps come back as map[string]any, sequences as []any,
integers as int, and floats as float64.

# YAML 1.1 and YAML 1.2

Unmarshal reads YAML 1.1 booleans by default, because graft's merge does. A
plain yes, Yes, YES, no, No, NO, on, On, ON, off, Off, or OFF decodes as a
boolean, and a quoted one stays a string. Other spellings of those words,
such as oFF, stay strings, and so do the single letters y and n.
yaml.v2 and spruce read y and n as booleans, as a value and as a key, and
graft deliberately doesn't.

UnmarshalWith with DecodeOptions{YAML12: true} reads those words as strings,
as YAML 1.2 does.

A string target holds a scalar as written, so 1.10 stays "1.10" and 0x1F
stays "0x1F", as in yaml.v2 and yaml.v3.

# Repeated keys

Unmarshal refuses a mapping that repeats a key, with an ErrorKindSyntax error
that names the line and column of both occurrences and never quotes the key.
UnmarshalWith with a DecodeOptions.OnDuplicateKey callback accepts a repeated
key in any mapping at any depth instead, keeps the last value, as yaml.v2
does, and calls the callback once for each later occurrence. Each report holds
a DuplicateKey with the path of the key and the positions of both occurrences.
Merge keys are never reported, and a key that a merge brings in repeats no key
written in the mapping. A mapping's keys and merge keys take effect in the
order written, on every target, so the value written last wins, as in spruce,
even when a key repeats. Unmarshal itself, and graft's merge, stay strict
about a repeated key, though both accept a second merge key in a mapping,
which spruce reads as a second merge.

Keys compare by their text under any tag or anchor, with two exceptions. A null key, such as ~ or null, and a string key whose text is null, such
as "null" or !!str null, repeat each other, because both are the key "null" in
a map[string]any, which holds only one of them. A null key and a string key
whose text is ~, such as "~" or !!str ~, don't repeat each other, and they
decode as the keys "null" and "~", as spruce reads them. Two null keys spelled
differently, such as null and ~, are never reported, and the later value wins,
as it does in spruce.

# Differences from yaml.v2 and yaml.v3

A program moving from yaml.v2 or yaml.v3 meets these differences.

Unmarshal refuses a map[interface{}]interface{} anywhere in the target type,
and the error says to use map[string]any. Every nested map in a generic
value is a map[string]any, so a caller never type-asserts a map twice.

Every error that Unmarshal and Node.Decode return is a *DecodeError, and it
carries a Line, a Column, a Kind, and a Msg. The text of a DecodeError never
quotes the input, so it is safe to log when the input holds credentials. For
that reason the messages are shorter than the ones yaml.v2 and yaml.v3 give,
which often print the offending scalar.

Marshal writes map keys in spruce's order with two spaces of indentation, as
graft merge does. MarshalWith with EncodeOptions{KeyOrder: sortkeys.Lexical}
writes keys in codepoint order instead.

Merge keys follow spruce wherever graft decodes to values, which covers
Unmarshal, UnmarshalWith, Node.Decode, and graft's merge. A mapping's pairs
take effect in the order written, and each one writes over the ones before
it. A << key writes the pairs of the map it names, so a merged value wins over
a key written before the << and loses to a key written after it. When the
value of << is a list of maps, an earlier map wins over a later one, and a
second << key in the mapping writes over the first. A << whose value is
neither a map nor a list of maps fails with an ErrorKindSyntax error at the
line of the <<, and so does a << whose value is an alias to a list, which
spruce refuses even though it takes the same list written in place. A plain
<< written after the explicit key indicator, as in "? <<", is a merge key
too, and a quoted << is an ordinary key.

A Node target merges nothing, so that it stays a lossless picture of the
text. It keeps each << key and its value as written, with an alias still an
AliasNode, and a program that walks or rewrites the tree sees the document
as it was. Node.Decode merges as Unmarshal does. With OnDuplicateKey set,
the << keys of one mapping in a Node become one, whose value lists the
sources of a later << before those of an earlier one, so a decode of the
Node reads the values a decode of the text reads.

yaml.v2 agrees with graft about the order of a list and about a key written
before or after the <<. yaml.v3 also lets an earlier map in the list win,
but a key written in the mapping always wins over a merged value there,
wherever the << sits.

A time.Time target accepts a quoted date such as "2001-12-14". yaml.v2 and
yaml.v3 refuse a quoted date that is not RFC 3339.

A named bool type, such as type Flag bool, reads YAML 1.2 booleans only, so
a plain yes is a type error for it. An unnamed bool field reads yes as true.

A !!str ~ or a !!str null keeps its text, so it decodes as "~" or "null",
as it does in spruce. A null key, such as ~ or null, decodes as the key ""
in a map[string]T target and as the key "null" in an any. A mapping that
holds a null key beside a string key whose text is null, such as "null" or
!!str null, is refused as a repeated key, as the section on repeated keys
describes.

A !!timestamp value keeps its text when it decodes into a string target or
an any, so a: !!timestamp 2001-12-14 gives the string "2001-12-14". A
time.Time target still parses it as a date.

A panic that the decoder raises on a target shape it cannot fill is
recovered and returned as an ErrorKindType DecodeError with Line 0. A panic
raised by a type's own UnmarshalYAML or MarshalYAML method is not the
decoder's, and it is not recovered, even when the method misuses package
reflect and the panic starts inside it.

A type with an UnmarshalText method receives the formatted text of a scalar,
not the source text, so 1.10 arrives as "1.1". A type whose UnmarshalYAML
method takes a byte slice, in goccy's form, receives the source text of its
value, without its comments, from Unmarshal, UnmarshalWith, and
Decoder.Decode when the target holds no Unmarshaler. Under Node.Decode, and
whenever the target also holds an Unmarshaler, it receives flow-style text,
which keeps every value but not the layout of the input. A method that takes
a callback, in goccy's other form, decodes the same values on either route.
We keep those forms working so that existing code runs unchanged, but
Unmarshaler is the supported hook, and a major release may stop calling the
others.

A whole float inside the int64 range marshals as integer digits, so 3.0
becomes 3 and 1e6 becomes 1000000. Reading that output into an any gives an
int. A whole float outside that range prints with an exponent, so 1e20
becomes 1.0e+20, and any other float keeps its decimal point. These outputs
differ from yaml.v2 for values at or above 1e6, which it writes as 1e+06.

Marshal chooses quotes by spruce's rules plus a few more of its own. It
double-quotes a string that ends in <<, a string that starts with -, ?, or :
followed by any character other than a space, such as -foo, ?x, or -#, and a
multi-line string whose first line starts with whitespace. Inside a field
tagged ,flow, a string in a list or map is double-quoted when it needs
quotes, and a string field that is itself tagged ,flow is single-quoted. A
key is written in the "? key" form when its double-quoted form would be
longer than 1024 bytes, so a plain key of 1023 bytes already takes it.

Marshal refuses a value that holds a Node, because writing a Node tree back
out is not supported. It also refuses a cycle and a key order other than the
zero value, sortkeys.Spruce, and sortkeys.Lexical. Each refusal is an error
that names a Go type and a field path, and it never quotes a value.

# Indentation and sequences

MarshalWith with EncodeOptions{Indent: n} writes each nested mapping, and
each block scalar's content, n columns past its parent key. Zero means 2,
and any value from 2 through 9 is accepted. Any other value is an error.
EncodeOptions{IndentSequence: true} also indents a sequence under a mapping
key, and a root sequence, by Indent columns, where the default leaves a
sequence at the column of its key.

# Ordered maps

A MapSlice is a list of MapItem values, each holding a Key and a Value, and
Marshal writes its items in the order they sit, at any depth, whatever
KeyOrder says. The Go maps inside its values still follow KeyOrder. A key
follows the rules for a map key, so a collection is refused, and so are
two items whose keys write the same text. Only MapSlice and *MapSlice are
written as ordered mappings. Unmarshal into a MapSlice is refused, because
the type is for encoding. Decode into a Node when the order of keys
matters on the way in.

# Streams of documents

An Encoder, from NewEncoder or NewEncoderWith, writes one document for each
call to Encode, with the bytes MarshalWith gives, and puts a "---" line
before every document after the first. Close ends the stream, writes
nothing, and makes later calls to Encode fail.

A Decoder, from NewDecoder or NewDecoderWith, reads one document for each
call to Decode into a pointer, as UnmarshalWith would, and returns io.EOF
when the documents run out. It reads all of its reader on the first call.
It skips a document that holds only comments, directives, or markers.
Lines in errors, in duplicate-key reports, and in Node values count from
the start of the stream. Only a framing error ends the stream for good.
A framing error is a reader error, invalid UTF-16 after a byte order mark,
or content after a "..." with no "---" before it. Any other error fails
only its own document, and the next call to Decode moves on to the next
one. An alias cannot point at an anchor in an earlier document, because
YAML scopes an anchor to its own document.

# Types that decode themselves

A type with an UnmarshalYAML(*Node) method is an Unmarshaler. Node.Decode,
Unmarshal, UnmarshalWith, and Decoder.Decode all call the method, with the
Node that holds the value, wherever a value of the type sits in the target.
The method reads the node's Line, Column, and Tag, or calls node.Decode,
usually into a private type of its own with no such method, so that it does
not call itself again. A null never reaches the method, whether it has no
tag or the tag !!null, as in yaml.v3. It decodes as it would for a type
without the method, so a struct field and the value at the root keep what
they held, a pointer below the root becomes nil, and a map value or a list
item becomes the zero value. A type that is a map key, or a named ,inline
field, is refused, because no method can run there. Embedding a type that
has the method gives the outer type the method too, so the outer type
decodes through it, and its other fields stay unset unless the method
fills them. When more than one struct field reads the same key, the method
does not run for that key, and the fields get a plain decode.

The error an Unmarshaler returns comes back as a *DecodeError, and Unwrap
on it returns the method's own error, so errors.Is and errors.As reach it.
Error never prints that cause. If goccy's own decode of the document fails
first, no method runs, and that error comes back even when a method would
also have failed.

# Known limitations

These behaviors follow from the decoder that graft shares with its merge.

A scalar with an explicit !!str, !!bool, !!int, !!float, !!null, or !!binary
tag reads as spruce reads it, in both modes, and graft merge gives the same
result. So !!str yes stays the word yes, !!int 0x1f is 31, and !!binary
decodes base64 to the text of its bytes. When the tag cannot hold its text,
as in a: !!int word, the document fails with an ErrorKindSyntax error that
names the tags and the tag's position and never quotes the text. A second
tag on one node and a tag on an alias are syntax errors too, as they are in
spruce.

A scalar under a tag that spruce doesn't resolve reads as its text when it
decodes into a string or a generic value, as it does in spruce. That covers
!!timestamp, a custom tag such as !foo, and the bare ! tag. So !foo yes
stays the word yes, and !!timestamp word stays the word rather than becoming
the zero time. A time.Time field still reads a date written under
!!timestamp.

A scalar tagged !!float over integer text reads as the float of that integer,
where spruce refuses it with "cannot decode a !!int scalar as !!float". So
a: !!float 3 is 3.0, and !!float -7, !!float 0x1F, and !!float "3" are -7.0,
31.0, and 3.0. This is a deliberate difference. !!int over float text, as in
a: !!int 3.0, is still a syntax error, as it is in spruce.

The decoder refuses a few tagged documents that spruce reads. A !!str or
other scalar tag on a flow or block collection is a syntax error, where
spruce ignores the tag. That holds with an anchor between the tag and the
collection, in either order and after a space or a tab, as in
a: !!str &n {k: v}. A !!set, !!omap, or !!pairs tag on a scalar, as in
a: !!set yes, is a syntax error as well, where spruce reads the text. And
a tag with nothing after it on the last line of a document that ends with
no line break, as in a: !!str or a: !foo, reads as null, because the
parser drops the tag before the decoder sees it, where spruce reads the
empty string.

A tag with no value before the next key or item at its own level, as in
"a: !foo\nb: 1" or "- !!str\n- 2", reads as spruce reads it. That holds
for every tag, with an anchor before or after the tag, and with a space,
a tab, a comment, or blank lines after them. The value is the empty
string under most tags and null under !!null. Under !!int, !!float, or
!!bool it is a syntax error that gives the tag's line and column, as it
is in spruce, and so are two tags or two anchors on the node, as in
"a: !x !y\nb: 1". A node on the next line that is indented under the
key or the item stays the tag's value, as in "a: !foo\n  x: 1". An
anchor with nothing after it at the very end of a document, as in a: &n,
is a syntax error, where spruce reads null.

A !!map or !!seq tag on a scalar, as in a: !!map x or a: !!seq yes, is a
syntax error too, where spruce ignores the tag and reads the scalar. A
tab after the tag fails the same way, and so does a tab followed by a
space, as in "a: !!map\t x". A tag or an anchor followed by a space and
then a tab on a mapping key, as in "!!str \tk: v" or "&n \tk: v", is a
syntax error as well, where spruce reads the key. With only spaces or
only tabs after the tag or the anchor, the key reads as spruce reads it.

An explicit key whose tag is alone on the "?" line, with the key's text on
the next line, as in "? !x\n  y\n: v", is a syntax error, where spruce
reads the key y with the value v.

A tag with nothing after it in a flow collection can read differently
from spruce, with no error. A !!seq tag before a comma, as in
a: {k: !!seq , w: z}, reads as an empty sequence, where spruce reads the
empty string. A !!int or !!bool tag there, as in a: [!!int , z], reads as
0 or false, where spruce fails. A tab in place of the space reads the
same way.

A custom, verbatim, or bare tag with nothing after it in a flow
collection reads as the empty string, as it does in spruce, when a
blank, an anchor, or a line break comes between the tag and the comma or
the collection's end, as in a: {k: !foo &n, w: z}. A few of those shapes
are syntax errors that give a line and a column, where spruce reads
them. One is a tag with a comma or a bracket right after it, as in
a: [!, z] or a: {k: !foo,w: z}. Spruce reads that character as part of
the tag's name, so it drops the item or reads the next value as the
tag's. Another is a verbatim tag that holds a comma, as in
a: [!<tag:x.y,2020:z> , z], and the last is a !!set tag, as in
a: {k: !!set &n, w: z}.

A block scalar whose last line ends the document with no line break,
and holds a tab inside the indentation, as in "a: |\n    x\n  \t", reads
as if that line were empty, where spruce fails. Unmarshal into a Node
fails on it as spruce does, and with a line break after that line,
Unmarshal fails too.

A tagged mapping key reads as a string key, because a decoded map's keys are
strings. So "!!bool yes: a" gives the key "true", and graft merge prints it
quoted, as "true": a, where spruce prints true: a. Untagged keys such as true
and 1 print quoted in the same way.

An anchored mapping key that is quoted or tagged !!str loses that quoting
when an alias uses it as a value. So in "&k !!str yes: 1\nb: *k", b reads as
true in the default mode, where spruce reads the word yes.

A !!binary scalar whose bytes aren't valid UTF-8, as in a: !!binary /w==,
decodes to a string that holds those bytes. Marshal and graft merge write
that string out as raw bytes, so the output isn't valid UTF-8, where spruce
writes it back as !!binary with its base64 text.

graft merge prints a whole float with a decimal point, so !!float 1e3 and an
untagged 1.0 print as 1000.0 and 1.0, where spruce prints 1000 and 1. The
decoded float64 is the same either way, and Marshal in this package writes
1000.

A literal or folded block scalar holding only a YAML 1.1 boolean word, as in
"a: |-\n  yes", reads as that word in both modes, as it does in spruce.

Unmarshal reads a document that starts with a UTF-16 byte order mark, in
either byte order, by converting it to UTF-8 first. A UTF-16 document with
no content, an odd number of bytes, or an unpaired surrogate is a syntax
error.

The column a Node or a DecodeError gives for a place on a continuation line
of a multi-line quoted scalar is approximate when the input uses tabs.

Node.Decode matches Unmarshal on a tree that Unmarshal returned, with a few
exceptions. They arise because the tree reader follows yaml.v3 while
Unmarshal follows goccy.

The two disagree on a node with two tags, as in a: !!str !!int 1, which
Unmarshal refuses and Decode reads as the integer. They disagree on a tag
written on an alias, as in b: !!str *x, which Unmarshal refuses and Decode
reads as the value the alias names. They also disagree on a tag with nothing
after it at the end of a document that ends with no line break, as in
a: !!str, which Unmarshal reads as null and Decode reads as the empty
string. And they disagree on a core tag over a node of another kind, as
in a: !!str {k: v} or a: !!map x, which Unmarshal refuses. Unmarshal into
a Node keeps that tag on the node, and Decode reads the node under it as
spruce does.

A Node that Unmarshal returned keeps its whole input document, and the first
Decode on that tree parses it one more time. The cost is one extra parse and
the memory of one document for each tree that is read.

A type that decodes through an UnmarshalYAML method that takes a callback,
or that keeps what it decodes in an unexported field, receives goccy's raw
values. In the default mode, a quoted or block scalar holding yes, no, on,
or off can reach that method with an internal marker character in front of
it when the target also holds a value of interface type.

# Stability

This package is stable, and the module's v1 promise applies to it. We add
exported names and never remove or change one. The exported fields of
DecodeError are part of that promise, but the text that Error returns may
change between releases. The package imports only the standard library,
goccy's go-yaml, graft's internal packages, and pkg/sortkeys with the
packages it imports. It never imports pkg/graft.
*/
package yaml

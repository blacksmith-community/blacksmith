package yamlnode

// StreamDocument is one document of a YAML stream, as SplitStream cuts
// it.
type StreamDocument struct {
	// Text is the document's own slice of the stream, with its line
	// breaks as LF, which parses as that document alone.
	Text string
	// LineOffset is what we add to a line of Text to get its line in the
	// stream.
	LineOffset int
	// Empty marks a document that holds no token other than comments,
	// directives, and the "---" and "..." markers.
	Empty bool
	// Err is the error yaml.v3 gives before the document starts, such as
	// for content that follows a "..." with no "---" before it, and nil
	// for most documents. Its lines count from the start of the stream.
	Err error
}

// SplitStream cuts src into one StreamDocument per document, the way
// yaml.v3 counts documents. It strips a leading UTF-8 byte order mark,
// which takes no line, and turns every line break into LF first, as the
// parse of each document does, so a line counts the same in a document's
// Text as in the stream. A document that a "---" starts is always there,
// even when it is Empty. A trailing chunk that holds only the comments
// after the last "..." comes back as an Empty document too.
func SplitStream(src string) []StreamDocument {
	text := string(normalizeLineBreaks(TrimBOM([]byte(src))))
	chunks := splitDocuments(text)
	docs := make([]StreamDocument, len(chunks))
	for i, c := range chunks {
		docs[i] = StreamDocument{
			Text:       c.text,
			LineOffset: max(c.lineOffset(), 0),
			Empty:      c.tail || !c.content,
			Err:        c.startErr,
		}
	}
	return docs
}

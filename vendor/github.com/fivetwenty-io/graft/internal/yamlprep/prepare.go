package yamlprep

// Prepare applies graft's goccy input workarounds in ParseYAML's order:
// the bare-dash rewrite, inject-key quoting, brace-placeholder quoting,
// and quoting of the column-1 scalars that start with "...". It returns
// the rewritten bytes and the 1-based lines whose bare "-" became "- ~".
// None of the rewrites adds or removes a newline, so line numbers in the
// result match the input.
func Prepare(data []byte) ([]byte, []int) {
	out, lines := BareDashRewrites(data)
	out = QuoteInjectKeys(out)
	out = QuoteBracePlaceholders(out)
	return QuoteEndMarkerScalars(out), lines
}

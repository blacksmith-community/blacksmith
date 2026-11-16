// Copyright © 2019 The Homeport Team
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.
//
// Ported from github.com/homeport/dyff v1.12.0 pkg/dyff/output_human.go
// (writeStringDiff, highlightByLine, highlightRemovals, highlightAdditions,
// LoadX509Certs, certificateSummaryAsYAML, isMultiLine,
// showWhitespaceCharacters) and pkg/dyff/core.go (isWhitespaceOnlyChange),
// and modified for graft: the settings are spruce's (certificates are
// inspected, no indent, no multiline prefixes, no multiline context lines,
// and a minor-change threshold of zero), and writeStringDiff's branches
// split into one helper each.

package humanreport

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/fivetwenty-io/graft/internal/yamldiff"
)

// writeStringDiff writes a change between two strings as a certificate
// change, a whitespace only change, a multiline change, a minor change
// with the changed runes highlighted, or a plain value change, in that
// order of preference.
func (r *reporter) writeStringDiff(output io.StringWriter, from, to string) {
	fromCertText, toCertText, err := loadX509Certs(from, to)

	switch {
	case err == nil:
		_, _ = output.WriteString(r.yellowf("%c certificate change\n", yamldiff.MODIFICATION))
		_, _ = output.WriteString(r.highlightByLine(fromCertText, toCertText))

	case isWhitespaceOnlyChange(from, to):
		_, _ = output.WriteString(r.yellowf("%c whitespace only change\n", yamldiff.MODIFICATION))
		writeTextBlocks(output, 0, r.width,
			r.red(createStringWithPrefix("- ", r.showWhitespaceCharacters(from), r.indent)),
			r.green(createStringWithPrefix("+ ", r.showWhitespaceCharacters(to), r.indent)),
		)

	case isMultiLine(from, to):
		r.writeMultilineDiff(output, from, to)

	case isMinorChange(from, to):
		_, _ = output.WriteString(r.yellowf("%c value change\n", yamldiff.MODIFICATION))
		diffs := diffmatchpatch.New().DiffMain(from, to, false)
		_, _ = output.WriteString(r.highlightRemovals(diffs))
		_, _ = output.WriteString(r.highlightAdditions(diffs))

	default:
		_, _ = output.WriteString(r.yellowf("%c value change\n", yamldiff.MODIFICATION))
		_, _ = output.WriteString(r.red(createStringWithPrefix("- ", from, r.indent)))
		_, _ = output.WriteString(r.green(createStringWithPrefix("+ ", to, r.indent)))
	}
}

// writeMultilineDiff writes a line by line diff of two texts. spruce asks
// for no context lines, so unchanged hunks are skipped.
func (r *reporter) writeMultilineDiff(output io.StringWriter, from, to string) {
	// create line by line diff
	dmp := diffmatchpatch.New()
	oldIdx, newIdx, lines := dmp.DiffLinesToChars(from, to)
	diff := dmp.DiffMain(oldIdx, newIdx, false)
	diff = dmp.DiffCharsToLines(diff, lines)

	var ins, del int
	var buf bytes.Buffer
	for _, d := range diff {
		// color and format each diff by type
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			buf.WriteString(r.green(createStringWithContinuousPrefix("+ ", d.Text, r.indent)))
			ins++

		case diffmatchpatch.DiffDelete:
			buf.WriteString(r.red(createStringWithContinuousPrefix("- ", d.Text, r.indent)))
			del++

		case diffmatchpatch.DiffEqual:
			// skip equal output, because the requested context is 0
			continue
		}
	}

	_, _ = output.WriteString(
		r.yellowf("%c value change in multiline text (%s, %s)\n",
			yamldiff.MODIFICATION, plural(ins, "insert"), plural(del, "deletion")))
	_, _ = output.WriteString(buf.String())
	_, _ = output.WriteString("\n")
}

// highlightByLine writes two certificate summaries side by side or
// stacked, with the lines that differ in full color and the others light.
func (r *reporter) highlightByLine(from, to string) string {
	fromLines := strings.Split(from, "\n")
	toLines := strings.Split(to, "\n")

	var buf bytes.Buffer

	if len(fromLines) == len(toLines) {
		for i := range fromLines {
			if fromLines[i] != toLines[i] {
				fromLines[i] = r.red(fromLines[i])
				toLines[i] = r.green(toLines[i])
			} else {
				fromLines[i] = r.lightRed(fromLines[i])
				toLines[i] = r.lightGreen(toLines[i])
			}
		}

		writeTextBlocks(&buf, 0, r.width,
			createStringWithPrefix(r.red("- "), strings.Join(fromLines, "\n"), r.indent),
			createStringWithPrefix(r.green("+ "), strings.Join(toLines, "\n"), r.indent))
	} else {
		writeTextBlocks(&buf, 0, r.width,
			r.red(createStringWithPrefix("- ", from, r.indent)),
			r.green(createStringWithPrefix("+ ", to, r.indent)),
		)
	}

	return buf.String()
}

// highlightRemovals writes the from-side of a minor change, with the
// removed runes in bold.
func (r *reporter) highlightRemovals(diffs []diffmatchpatch.Diff) string {
	var buf bytes.Buffer

	buf.WriteString(r.redf("%s- ", strings.Repeat(" ", r.indent)))
	for _, part := range diffs {
		switch part.Type {
		case diffmatchpatch.DiffEqual:
			buf.WriteString(r.lightRed(part.Text))

		case diffmatchpatch.DiffDelete:
			buf.WriteString(r.boldRed(part.Text))

		default:
			// Insertions belong to the to-side.
		}
	}

	buf.WriteString("\n")
	return buf.String()
}

// highlightAdditions writes the to-side of a minor change, with the added
// runes in bold.
func (r *reporter) highlightAdditions(diffs []diffmatchpatch.Diff) string {
	var buf bytes.Buffer

	buf.WriteString(r.greenf("%s+ ", strings.Repeat(" ", r.indent)))
	for _, part := range diffs {
		switch part.Type {
		case diffmatchpatch.DiffEqual:
			buf.WriteString(r.lightGreen(part.Text))

		case diffmatchpatch.DiffInsert:
			buf.WriteString(r.boldGreen(part.Text))

		default:
			// Deletions belong to the from-side.
		}
	}

	buf.WriteString("\n")
	return buf.String()
}

// loadX509Certs tries to load the provided strings as a cert each and
// returns a textual representation of the certs, or an error if the
// strings are not X509 certs.
func loadX509Certs(from, to string) (string, string, error) {
	fromDecoded, _ := pem.Decode([]byte(from))
	if fromDecoded == nil {
		return "", "", fmt.Errorf("string '%s' is no PEM string", from)
	}

	toDecoded, _ := pem.Decode([]byte(to))
	if toDecoded == nil {
		return "", "", fmt.Errorf("string '%s' is no PEM string", to)
	}

	fromCert, err := x509.ParseCertificate(fromDecoded.Bytes)
	if err != nil {
		return "", "", err
	}

	toCert, err := x509.ParseCertificate(toDecoded.Bytes)
	if err != nil {
		return "", "", err
	}

	return certificateSummaryAsYAML(fromCert),
		certificateSummaryAsYAML(toCert),
		nil
}

// certificateSummaryAsYAML creates a YAML hash from a certificate that
// shows only a few important fields:
//
//	Common Name: www.example.com
//	Organization: Company Name
//	Organization Unit: Org
//	Locality: Portland
//	State: Oregon
//	Country: US
//	Valid From: April 2, 2018
//	Valid To: April 2, 2019
//	Issuer: www.example.com, Company Name
//	Serial Number: 14581103526614300972 (0xca5a7c67490a792c)
func certificateSummaryAsYAML(cert *x509.Certificate) string {
	const template = `Subject:
  Common Name: %s
  Organization: %s
  Organization Unit: %s
  Locality: %s
  State: %s
  Country: %s
Validity Period:
  NotBefore: %s
  NotAfter: %s
Issuer: %s, %s
Serial Number: %d (%#x)
`

	return fmt.Sprintf(template,
		cert.Subject.CommonName,
		strings.Join(cert.Subject.Organization, " "),
		strings.Join(cert.Subject.OrganizationalUnit, " "),
		strings.Join(cert.Subject.Locality, " "),
		strings.Join(cert.Subject.Province, " "),
		strings.Join(cert.Subject.Country, " "),
		cert.NotBefore.Format("Jan 2 15:04:05 2006 MST"),
		cert.NotAfter.Format("Jan 2 15:04:05 2006 MST"),
		cert.Issuer.CommonName, strings.Join(cert.Issuer.Organization, " "),
		cert.SerialNumber, cert.SerialNumber,
	)
}

// isWhitespaceOnlyChange reports whether the strings match once leading
// and trailing spaces and newlines are trimmed.
func isWhitespaceOnlyChange(from, to string) bool {
	return strings.Trim(from, " \n") == strings.Trim(to, " \n")
}

// isMultiLine reports whether either string spans more than one line.
func isMultiLine(from, to string) bool {
	return strings.Contains(from, "\n") || strings.Contains(to, "\n")
}

// showWhitespaceCharacters makes newlines and spaces visible as bold
// arrows and dots.
func (r *reporter) showWhitespaceCharacters(text string) string {
	return strings.ReplaceAll(
		strings.ReplaceAll(text,
			"\n",
			r.bold("↵\n")),
		" ",
		r.bold("·"),
	)
}

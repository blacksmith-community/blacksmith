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
// (createStringWithContinuousPrefix, createStringWithPrefix,
// stringArrayLen, writeTextBlocks, CreateTableStyleString) and from
// github.com/gonvenience/text v1.0.10 text.go (Plural), and modified for
// graft: the terminal width is a parameter, the NoTableStyle option is
// gone because spruce never sets it, and the plain text length comes from
// termstyle.PlainLen.

// Package humanreport renders a yamldiff.Report exactly as homeport/dyff
// v1.12.0's HumanReport does with spruce's settings (OmitHeader, no
// indent, table layout allowed, no context lines), so `graft diff` prints
// the same bytes as `spruce diff`.
package humanreport

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/termstyle"
)

// plural returns a string with the number and noun in either singular or
// plural form. With one word the plural adds an s, and with two or more the
// second word is the irregular plural. Counts below thirteen are spelled out.
func plural(n int, words ...string) string {
	numbers := [...]string{"no", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}

	var number string
	if n < len(numbers) {
		number = numbers[n]
	} else {
		number = strconv.Itoa(n)
	}

	switch len(words) {
	case 1:
		if n == 1 {
			return fmt.Sprintf("%s %s", number, words[0])
		}

		return fmt.Sprintf("%s %ss", number, words[0])

	default:
		if n == 1 {
			return fmt.Sprintf("%s %s", number, words[0])
		}

		return fmt.Sprintf("%s %s", number, words[1])
	}
}

// createStringWithContinuousPrefix adds the prefix to every line of the
// object's string form. The result always ends with a newline.
func createStringWithContinuousPrefix(prefix string, obj interface{}, indent int) string {
	trimmed := strings.TrimSuffix(fmt.Sprint(obj), "\n") // avoid an additional empty line if the original ends with a newline
	var buf bytes.Buffer
	for _, line := range strings.Split(trimmed, "\n") {
		buf.WriteString(strings.Repeat(" ", indent))
		buf.WriteString(prefix)
		buf.WriteString(line)
		buf.WriteString("\n") // always adds a newline, even if the original has none
	}
	return buf.String()
}

// createStringWithPrefix puts the prefix before the first line of the
// object's string form and pads the following lines to match its width.
func createStringWithPrefix(prefix string, obj interface{}, indent int) string {
	var buf bytes.Buffer
	for i, line := range strings.Split(fmt.Sprintf("%v", obj), "\n") {
		if i == 0 {
			buf.WriteString(strings.Repeat(" ", indent))
			buf.WriteString(prefix)
		} else {
			buf.WriteString(strings.Repeat(" ", termstyle.PlainLen(prefix)+indent))
		}

		buf.WriteString(line)
		buf.WriteString("\n")
	}

	return buf.String()
}

// stringArrayLen sums the plain text lengths of the entries.
func stringArrayLen(list []string) int {
	result := 0
	for _, entry := range list {
		result += termstyle.PlainLen(entry)
	}

	return result
}

// writeTextBlocks writes the blocks into buf in a table style, with each
// block a column, or in a stacked style, with each block a row, depending
// on whether the table would fit within width.
func writeTextBlocks(buf io.StringWriter, indent, width int, blocks ...string) {
	const separator = "   "

	// Calculate the theoretical maximum line length if the blocks were
	// rendered next to each other.
	theoreticalMaxLineLength := indent + ((len(blocks) - 1) * termstyle.PlainLen(separator))
	for _, block := range blocks {
		maxLineLengthInBlock := 0
		for _, line := range strings.Split(block, "\n") {
			if lineLength := termstyle.PlainLen(line); maxLineLengthInBlock < lineLength {
				maxLineLengthInBlock = lineLength
			}
		}

		theoreticalMaxLineLength += maxLineLengthInBlock
	}

	// If the blocks side by side would surpass the terminal width, fall back
	// to the stacked style.
	if theoreticalMaxLineLength > width {
		for _, block := range blocks {
			lines := strings.Split(block, "\n")
			for _, line := range lines {
				_, _ = buf.WriteString(strings.Repeat(" ", indent))
				_, _ = buf.WriteString(line)
				_, _ = buf.WriteString("\n")
			}
		}
	} else {
		_, _ = buf.WriteString(createTableStyleString(separator, indent, blocks...))
	}
}

// createTableStyleString arranges the multi-line columns next to each other
// with padding, so the text blocks line up as a table.
func createTableStyleString(separator string, indent int, columns ...string) string {
	cols := len(columns)
	rows := -1
	maxLen := make([]int, cols)

	for i, col := range columns {
		lines := strings.Split(col, "\n")
		if noOfLines := len(lines); noOfLines > rows {
			rows = noOfLines
		}

		for _, line := range lines {
			if length := termstyle.PlainLen(line); length > maxLen[i] {
				maxLen[i] = length
			}
		}
	}

	mtrx := make([][]string, 0, max(rows, 0))
	for x := 0; x < rows; x++ {
		mtrx = append(mtrx, make([]string, cols))
		for y := 0; y < cols; y++ {
			mtrx[x][y] = strings.Repeat(" ", maxLen[y]+indent)
		}
	}

	for i, col := range columns {
		for j, line := range strings.Split(col, "\n") {
			mtrx[j][i] = strings.Repeat(" ", indent) +
				line +
				strings.Repeat(" ", maxLen[i]-termstyle.PlainLen(line))
		}
	}

	var buf bytes.Buffer
	for i, row := range mtrx {
		buf.WriteString(strings.TrimRight(strings.Join(row, separator), " "))

		if i < len(mtrx)-1 {
			buf.WriteString("\n")
		}
	}

	return buf.String()
}

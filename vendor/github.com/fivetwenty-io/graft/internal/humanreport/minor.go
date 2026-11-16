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
// (isMinorChange) and modified for graft: the levenshtein distance call
// becomes a banded insertion and deletion check that gives the same answer.

package humanreport

// minorChangeLimit is the edit distance at or below which dyff treats a
// string change as minor and highlights the changed runes.
const minorChangeLimit = 4

// isMinorChange reports whether dyff's isMinorChange (output_human.go
// 628-640) is true under graft's settings. dyff measures the levenshtein
// distance with insertions and deletions costing 1 and substitutions 2, so
// the distance equals the number of rune insertions and deletions needed,
// and with a minor-change threshold of 0 only its "distance <= 4" shortcut
// can succeed. A banded dynamic program over runes answers that in linear
// time, which matters for long single-line values such as encoded blobs.
func isMinorChange(from, to string) bool {
	a, b := []rune(from), []rune(to)
	if diff := len(a) - len(b); diff > minorChangeLimit || diff < -minorChangeLimit {
		return false
	}
	const width = 2*minorChangeLimit + 1
	const over = minorChangeLimit + 1
	prev, cur := make([]int, width), make([]int, width)
	for k := range prev {
		j := k - minorChangeLimit // column for row 0
		prev[k] = over
		if j >= 0 && j <= len(b) {
			prev[k] = j
		}
	}
	for i := 1; i <= len(a); i++ {
		rowMin := over
		for k := 0; k < width; k++ {
			j := i + k - minorChangeLimit
			cur[k] = indelCell(a, b, i, j, k, prev, cur)
			if cur[k] < rowMin {
				rowMin = cur[k]
			}
		}
		if rowMin > minorChangeLimit {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(b)-len(a)+minorChangeLimit] <= minorChangeLimit
}

// indelCell computes one banded cell: the cheapest of a match on the
// diagonal, a deletion from the row above, and an insertion from the
// cell to the left, capped just above the limit.
func indelCell(a, b []rune, i, j, k int, prev, cur []int) int {
	const over = minorChangeLimit + 1
	switch {
	case j < 0 || j > len(b):
		return over
	case j == 0:
		return min(i, over)
	}
	best := over
	if a[i-1] == b[j-1] {
		best = prev[k]
	}
	if k+1 < len(prev) && prev[k+1]+1 < best {
		best = prev[k+1] + 1
	}
	if k > 0 && cur[k-1]+1 < best {
		best = cur[k-1] + 1
	}
	return min(best, over)
}

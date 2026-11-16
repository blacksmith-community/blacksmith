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
// Ported from github.com/homeport/dyff v1.12.0 (models.go) and modified
// for graft: details carry yamlnode trees, and the detail kinds have
// their own type.

package yamldiff

import "github.com/fivetwenty-io/graft/internal/yamlnode"

// DetailKind tells what kind of difference a Detail describes.
type DetailKind rune

// The kinds of differences, with dyff's rune values.
const (
	ADDITION     DetailKind = '+'
	REMOVAL      DetailKind = '-'
	MODIFICATION DetailKind = '±'
	ORDERCHANGE  DetailKind = '⇆'
)

// Detail holds the kind of one difference and the values on each side.
// An addition has no From, and a removal has no To.
//
// Indexes is set only on a list addition or removal. It holds, for each
// entry of the fragment in To or From, the position of that entry in the
// list it belongs to, which is the new list for an addition and the old
// list for a removal. The report never prints it.
type Detail struct {
	Kind    DetailKind
	From    *yamlnode.Node
	To      *yamlnode.Node
	Indexes []int
}

// Diff holds every difference found at one path. A document order change
// has no path.
type Diff struct {
	Path    *Path
	Details []Detail
}

// Report holds the compared inputs and the differences between them.
type Report struct {
	From  InputFile
	To    InputFile
	Diffs []Diff
}

// Copyright © 2018 The Homeport Team
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
// Ported from github.com/gonvenience/ytbx v1.5.0 (input.go) and modified
// for graft.

package yamldiff

// LocationKind says how ytbx would color an input location.
type LocationKind int

// The kinds of input location.
const (
	// LocationPlain is a location that is neither stdin, a file, nor a URI.
	LocationPlain LocationKind = iota
	// LocationStdin is the dash that stands for standard input.
	LocationStdin
	// LocationFile is a location that exists on disk.
	LocationFile
	// LocationURI is a location that parses as a request URI.
	LocationURI
)

// LocationStyler renders a location for an error message. It receives the
// raw location and its kind, and decides what to print.
type LocationStyler func(kind LocationKind, location string) string

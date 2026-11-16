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
// Copyright (c) 2014 Mitchell Hashimoto
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
// Ported from github.com/gonvenience/term v1.0.5 (term.go), the terminal
// tests in github.com/gonvenience/bunt v1.4.3 (bunt.go), and the process name
// parsing in github.com/mitchellh/go-ps v1.0.0 (process_linux.go), and
// modified for graft: the checks take their environment and output file as
// arguments instead of reading process-wide state, and the garden test reads
// /proc/1/stat directly.

package termstyle

import (
	"os"
	"runtime"
	"strings"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"
)

// TrueColorFromEnv reports whether the terminal advertises 24-bit color.
// Like gonvenience/term, it trusts only COLORTERM=truecolor or 24bit, and
// TERM never matters.
func TrueColorFromEnv(getenv func(string) string) bool {
	switch getenv("COLORTERM") {
	case "truecolor", "24bit":
		return true
	default:
		return false
	}
}

// StdoutColorCapable reports whether bunt would color output written to
// f with no override: never on a Windows console outside Cygwin, and
// otherwise only when f is a terminal or a Cygwin terminal. NO_COLOR and
// TERM=dumb are left to ansi.ResolveColor.
func StdoutColorCapable(f *os.File) bool {
	fd := f.Fd()
	return colorCapable(runtime.GOOS, isatty.IsTerminal(fd), isatty.IsCygwinTerminal(fd))
}

// colorCapable makes the decision for StdoutColorCapable from the operating
// system name and the two terminal probes, so tests can cover the Windows
// and Cygwin branches on any host.
func colorCapable(goos string, isTerminal, isCygwin bool) bool {
	if goos == "windows" && !isCygwin {
		return false
	}
	return isTerminal || isCygwin
}

// TerminalWidth returns the width gonvenience/term would report for f:
// 120 inside a garden container (Concourse), even when f is a pipe;
// otherwise the column count of f, which is 0 under a pty with no size;
// and 80 when f is not a terminal.
func TerminalWidth(f *os.File) int {
	if name, err := pid1Name(); err == nil && name == "garden-init" {
		return 120
	}
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 80
	}
	return width
}

// pid1Name returns the executable name of process 1. Tests replace it.
var pid1Name = readPid1Name

// parseStatName returns the text between the first "(" and the next ")"
// in a /proc/<pid>/stat line, as go-ps does, or "" when there is none.
func parseStatName(stat string) string {
	start := strings.IndexByte(stat, '(')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(stat[start+1:], ')')
	if end < 0 {
		return ""
	}
	return stat[start+1 : start+1+end]
}

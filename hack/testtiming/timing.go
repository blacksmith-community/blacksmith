package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

// defaultTop is how many entries each list shows unless -top says otherwise.
const defaultTop = 15

// maxEventLine is the longest line of go test -json output the reader
// takes. A test that prints a very long line gets it split into several
// events, so this only has to cover one event.
const maxEventLine = 16 * 1024 * 1024

// timing is how long one package, test, or UI file took, and whether it
// failed.
type timing struct {
	name    string
	elapsed time.Duration
	failed  bool
}

// report is everything one run measured.
type report struct {
	packages []timing
	tests    []timing
	uiFiles  []timing
	haveUI   bool
}

// goEvent is the part of a go test -json event the report reads.
type goEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
}

// goRun is what readGoEvents found in a go test -json stream.
type goRun struct {
	packages []timing
	tests    []timing
}

// readGoEvents reads go test -json output and returns the elapsed time of
// every package and every top-level test that passed or failed. Subtests
// are left out, because their parent's time already holds theirs. A line
// that is not an event, as a build error can print, is skipped.
func readGoEvents(input io.Reader) (goRun, error) {
	var run goRun

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), maxEventLine)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}

		var event goEvent

		err := json.Unmarshal(line, &event)
		if err != nil {
			continue
		}

		if event.Action != "pass" && event.Action != "fail" {
			continue
		}

		entry := timing{elapsed: seconds(event.Elapsed), failed: event.Action == "fail"}

		switch {
		case event.Test == "":
			entry.name = event.Package
			run.packages = append(run.packages, entry)
		case !strings.Contains(event.Test, "/"):
			entry.name = event.Package + " " + event.Test
			run.tests = append(run.tests, entry)
		}
	}

	err := scanner.Err()
	if err != nil {
		return goRun{}, fmt.Errorf("reading the go test -json output: %w", err)
	}

	slowestFirst(run.packages)
	slowestFirst(run.tests)

	return run, nil
}

// junitSuite is one testsuite element of a JUnit report.
type junitSuite struct {
	Name     string  `xml:"name,attr"`
	File     string  `xml:"file,attr"`
	Time     float64 `xml:"time,attr"`
	Failures int     `xml:"failures,attr"`
	Errors   int     `xml:"errors,attr"`
}

// junitReport is the root of the JUnit report bun test writes, which
// holds one testsuite element for each test file.
type junitReport struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

// readJUnit reads the JUnit report bun test writes and returns how long
// each test file took.
func readJUnit(input io.Reader) ([]timing, error) {
	var parsed junitReport

	err := xml.NewDecoder(input).Decode(&parsed)
	if err != nil {
		return nil, fmt.Errorf("reading the JUnit report: %w", err)
	}

	files := make([]timing, 0, len(parsed.Suites))

	for _, suite := range parsed.Suites {
		name := suite.File
		if name == "" {
			name = suite.Name
		}

		files = append(files, timing{name: name, elapsed: seconds(suite.Time), failed: suite.Failures+suite.Errors > 0})
	}

	slowestFirst(files)

	return files, nil
}

// printReport writes the slowest top entries of each list, slowest
// first. An empty list prints "none", and a run without a UI report says
// the UI files were not measured.
func printReport(out io.Writer, top int, measured report) {
	printList(out, "Slowest Go packages", top, measured.packages)
	printList(out, "Slowest Go tests", top, measured.tests)

	if !measured.haveUI {
		_, _ = fmt.Fprintf(out, "Slowest UI files\n  not measured\n")

		return
	}

	printList(out, "Slowest UI files", top, measured.uiFiles)
}

func printList(out io.Writer, title string, top int, entries []timing) {
	_, _ = fmt.Fprintln(out, title)

	if len(entries) == 0 {
		_, _ = fmt.Fprintln(out, "  none")
		_, _ = fmt.Fprintln(out)

		return
	}

	sorted := append([]timing(nil), entries...)
	slowestFirst(sorted)

	for index, entry := range sorted {
		if index == top {
			break
		}

		failed := ""
		if entry.failed {
			failed = "  (failed)"
		}

		_, _ = fmt.Fprintf(out, "  %8.2fs  %s%s\n", entry.elapsed.Seconds(), entry.name, failed)
	}

	_, _ = fmt.Fprintln(out)
}

// slowestFirst sorts entries by elapsed time, slowest first, and by name
// among equal times, so two runs list the same entries in the same order.
func slowestFirst(entries []timing) {
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].elapsed != entries[right].elapsed {
			return entries[left].elapsed > entries[right].elapsed
		}

		return entries[left].name < entries[right].name
	})
}

func seconds(value float64) time.Duration {
	return time.Duration(math.Round(value * float64(time.Second)))
}

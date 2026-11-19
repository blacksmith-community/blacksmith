// Command testtiming prints the slowest Go packages, Go tests, and UI test
// files of one test run, so two runs can be compared. make test-timing
// runs the suites and then this command.
//
// Usage:
//
//	go run ./hack/testtiming -go go-test.json [-ui ui-junit.xml] [-top 15]
//
// The -go file is the output of go test -json, and the -ui file is the
// JUnit report bun test writes with --reporter=junit. The command lists
// what it finds, slowest first, and never fails because nothing is slow.
// It fails only when a file cannot be read.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

var errNoGoReport = errors.New("-go names no go test -json output")

func main() {
	err := run(os.Args[1:], os.Stdout)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "testtiming:", err)

		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("testtiming", flag.ContinueOnError)
	goPath := flags.String("go", "", "the go test -json output to read")
	uiPath := flags.String("ui", "", "the JUnit report of bun test to read, if any")
	top := flags.Int("top", defaultTop, "how many entries each list shows")

	err := flags.Parse(args)
	if err != nil {
		return fmt.Errorf("parsing the arguments: %w", err)
	}

	if *goPath == "" {
		return errNoGoReport
	}

	goFile, err := os.Open(*goPath)
	if err != nil {
		return fmt.Errorf("opening the go test output: %w", err)
	}

	defer func() { _ = goFile.Close() }()

	goTimes, err := readGoEvents(goFile)
	if err != nil {
		return err
	}

	measured := report{packages: goTimes.packages, tests: goTimes.tests}

	if *uiPath != "" {
		uiFile, err := os.Open(*uiPath)
		if err != nil {
			return fmt.Errorf("opening the UI report: %w", err)
		}

		defer func() { _ = uiFile.Close() }()

		measured.uiFiles, err = readJUnit(uiFile)
		if err != nil {
			return err
		}

		measured.haveUI = true
	}

	printReport(out, *top, measured)

	return nil
}

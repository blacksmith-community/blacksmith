package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

const goEvents = `{"Action":"start","Package":"blacksmith/a"}
{"Action":"run","Package":"blacksmith/a","Test":"TestSlow"}
{"Action":"pass","Package":"blacksmith/a","Test":"TestSlow/sub","Elapsed":2.5}
{"Action":"pass","Package":"blacksmith/a","Test":"TestSlow","Elapsed":3}
{"Action":"fail","Package":"blacksmith/a","Test":"TestBroken","Elapsed":0.25}
{"Action":"skip","Package":"blacksmith/a","Test":"TestSkipped","Elapsed":0}
{"Action":"fail","Package":"blacksmith/a","Elapsed":4.5}
not json, as a build error prints
{"Action":"pass","Package":"blacksmith/b","Test":"TestQuick","Elapsed":0.01}
{"Action":"pass","Package":"blacksmith/b","Elapsed":0.5}
{"Action":"skip","Package":"blacksmith/c","Elapsed":0}
`

func TestReadGoEventsTimesPackagesAndTopLevelTests(t *testing.T) {
	t.Parallel()

	run, err := readGoEvents(strings.NewReader(goEvents))
	if err != nil {
		t.Fatalf("readGoEvents: %v", err)
	}

	wantPackages := []timing{
		{name: "blacksmith/a", elapsed: 4500 * time.Millisecond, failed: true},
		{name: "blacksmith/b", elapsed: 500 * time.Millisecond},
	}
	expectTimings(t, "packages", run.packages, wantPackages)

	wantTests := []timing{
		{name: "blacksmith/a TestSlow", elapsed: 3 * time.Second},
		{name: "blacksmith/a TestBroken", elapsed: 250 * time.Millisecond, failed: true},
		{name: "blacksmith/b TestQuick", elapsed: 10 * time.Millisecond},
	}
	expectTimings(t, "tests", run.tests, wantTests)
}

const junitFixture = `<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="bun test" tests="3" failures="1" time="3.5">
  <testsuite name="tests/ui/quick.test.js" file="tests/ui/quick.test.js" tests="1" failures="0" time="0.5">
    <testcase name="a quick test" time="0.5" file="tests/ui/quick.test.js" />
  </testsuite>
  <testsuite name="tests/ui/slow.test.js" file="tests/ui/slow.test.js" tests="2" failures="1" time="3">
    <testsuite name="a describe block" file="tests/ui/slow.test.js" tests="2" failures="1" time="2.9">
      <testcase name="a slow test" time="2.9" file="tests/ui/slow.test.js">
        <failure type="AssertionError" />
      </testcase>
    </testsuite>
  </testsuite>
</testsuites>
`

func TestReadJUnitTimesEachFile(t *testing.T) {
	t.Parallel()

	files, err := readJUnit(strings.NewReader(junitFixture))
	if err != nil {
		t.Fatalf("readJUnit: %v", err)
	}

	want := []timing{
		{name: "tests/ui/slow.test.js", elapsed: 3 * time.Second, failed: true},
		{name: "tests/ui/quick.test.js", elapsed: 500 * time.Millisecond},
	}
	expectTimings(t, "UI files", files, want)
}

func TestReadJUnitRefusesAReportThatIsNotJUnit(t *testing.T) {
	t.Parallel()

	_, err := readJUnit(strings.NewReader("this is not XML"))
	if err == nil {
		t.Fatal("readJUnit took text that is not a JUnit report")
	}
}

func TestPrintReportListsTheSlowestFirstAndCapsEachList(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	printReport(&out, 1, report{
		packages: []timing{{name: "blacksmith/b", elapsed: time.Second}, {name: "blacksmith/a", elapsed: 2 * time.Second, failed: true}},
		tests:    []timing{{name: "blacksmith/a TestSlow", elapsed: 1500 * time.Millisecond}},
		uiFiles:  []timing{{name: "tests/ui/slow.test.js", elapsed: 3 * time.Second}},
		haveUI:   true,
	})

	text := out.String()

	for _, want := range []string{
		"Slowest Go packages",
		"2.00s  blacksmith/a  (failed)",
		"Slowest Go tests",
		"1.50s  blacksmith/a TestSlow",
		"Slowest UI files",
		"3.00s  tests/ui/slow.test.js",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the report has no %q:\n%s", want, text)
		}
	}

	if strings.Contains(text, "blacksmith/b") {
		t.Errorf("the report lists more packages than its limit of one:\n%s", text)
	}
}

func TestPrintReportSaysSoWhenAListIsEmpty(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	printReport(&out, defaultTop, report{})

	text := out.String()

	if got := strings.Count(text, "none"); got != 2 {
		t.Errorf("the empty Go lists printed %d \"none\" lines, not 2:\n%s", got, text)
	}

	if !strings.Contains(text, "Slowest UI files\n  not measured") {
		t.Errorf("the report does not say the UI files were not measured:\n%s", text)
	}
}

func expectTimings(t *testing.T, what string, got, want []timing) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("%s: got %d timings %+v, want %d %+v", what, len(got), got, len(want), want)
	}

	for index := range want {
		if got[index] != want[index] {
			t.Errorf("%s[%d]: got %+v, want %+v", what, index, got[index], want[index])
		}
	}
}

func TestRunReadsBothReports(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	goPath := dir + "/go.json"
	uiPath := dir + "/ui.xml"

	err := os.WriteFile(goPath, []byte(goEvents), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(uiPath, []byte(junitFixture), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	err = run([]string{"-go", goPath, "-ui", uiPath, "-top", "2"}, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, want := range []string{"4.50s  blacksmith/a  (failed)", "3.00s  blacksmith/a TestSlow", "3.00s  tests/ui/slow.test.js  (failed)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the output has no %q:\n%s", want, out.String())
		}
	}
}

func TestRunNeedsTheGoReport(t *testing.T) {
	t.Parallel()

	err := run(nil, io.Discard)
	if !errors.Is(err, errNoGoReport) {
		t.Fatalf("run without -go returned %v, not %v", err, errNoGoReport)
	}
}

func TestRunSucceedsWhenNothingRan(t *testing.T) {
	t.Parallel()

	goPath := t.TempDir() + "/go.json"

	err := os.WriteFile(goPath, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	err = run([]string{"-go", goPath}, &out)
	if err != nil {
		t.Fatalf("run over an empty go test output: %v", err)
	}

	if !strings.Contains(out.String(), "Slowest Go packages\n  none") {
		t.Errorf("the output does not say no package ran:\n%s", out.String())
	}
}

package scripts_test

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"blacksmith/internal/ops/scripts"
)

const (
	testTaskID  = "0f1e2d3c-4b5a-4968-8776-655443322110"
	otherTaskID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	testHash    = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// shapeCase is one validator input and whether the validator accepts it.
type shapeCase struct {
	value string
	ok    bool
}

func taskCases() []shapeCase {
	return []shapeCase{
		{testTaskID, true},
		{otherTaskID, true},
		{"00000000-0000-0000-0000-000000000000", true},
		{strings.ToUpper(testTaskID), false},
		{"{" + testTaskID + "}", false},
		{strings.ReplaceAll(testTaskID, "-", ""), false},
		{"urn:uuid:" + testTaskID, false},
		{testTaskID + "\n", false},
		{testTaskID[:35], false},
		{testTaskID + "0", false},
		{"0f1e2d3c-4b5a-4968-8776-65544332211g", false},
		{"../" + testTaskID[3:], false},
		{"", false},
	}
}

func hashCases() []shapeCase {
	return []shapeCase{
		{testHash, true},
		{strings.Repeat("0", 64), true},
		{strings.ToUpper(testHash), false},
		{testHash[:63], false},
		{testHash + "0", false},
		{testHash + "\n", false},
		{testHash[:63] + "g", false},
		{"sha256:" + testHash, false},
		{"", false},
	}
}

// checkShape compares a validator's answer with the expected one. A
// refusal must wrap ErrRefused and must not repeat the input.
func checkShape(t *testing.T, name string, tc shapeCase, err error) {
	t.Helper()

	if tc.ok {
		if err != nil {
			t.Errorf("%s(%q) refused a valid input: %v", name, tc.value, err)
		}

		return
	}

	if err == nil {
		t.Errorf("%s(%q) accepted an input it must refuse", name, tc.value)

		return
	}

	if !errors.Is(err, scripts.ErrRefused) {
		t.Errorf("%s(%q) error %v is not ErrRefused", name, tc.value, err)
	}

	if tc.value != "" && strings.Contains(err.Error(), tc.value) {
		t.Errorf("%s(%q) error repeats its input: %v", name, tc.value, err)
	}

	if !strings.HasPrefix(err.Error(), "scripts: ") {
		t.Errorf("%s(%q) error %q lacks the scripts: prefix", name, tc.value, err)
	}
}

func TestValidTaskID(t *testing.T) {
	t.Parallel()

	for _, tc := range taskCases() {
		checkShape(t, "ValidTaskID", tc, scripts.ValidTaskID(tc.value))
	}
}

func TestValidHash(t *testing.T) {
	t.Parallel()

	for _, tc := range hashCases() {
		checkShape(t, "ValidHash", tc, scripts.ValidHash(tc.value))
	}
}

func TestSudoCheckCommand(t *testing.T) {
	t.Parallel()

	if scripts.SudoCheckCommand != "sudo -n true" {
		t.Errorf("SudoCheckCommand = %q, want sudo -n true", scripts.SudoCheckCommand)
	}
}

// libCheck runs one function of the base library alone, so the test shows
// the base is self-contained, and returns its exit code.
func libCheck(t *testing.T, function, value string) int {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "bash", "-c", string(scripts.Library())+"\n"+function+` "$@"`, "_", value) //nolint:gosec // fixed test program text
	cmd.Env = []string{}

	err := cmd.Run()
	if err == nil {
		return 0
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running lib.sh: %v", err)
	}

	return exit.ExitCode()
}

func TestValidatorShapeParityWithBash(t *testing.T) {
	t.Parallel()
	requireLinux(t)

	for _, tc := range taskCases() {
		want := scripts.ExitRefused
		if tc.ok {
			want = scripts.ExitOK
		}

		if got := libCheck(t, "valid_task", tc.value); got != want {
			t.Errorf("valid_task %q exited %d, want %d", tc.value, got, want)
		}
	}

	for _, tc := range hashCases() {
		want := scripts.ExitUsage
		if tc.ok {
			want = scripts.ExitOK
		}

		if got := libCheck(t, "valid_hash", tc.value); got != want {
			t.Errorf("valid_hash %q exited %d, want %d", tc.value, got, want)
		}
	}
}

// requireLinux skips a test that runs the library, because macOS ships
// bash 3.2 and BSD tools.
func requireLinux(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("the helper scripts need Linux tools; run this test in the golang container")
	}

	_, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
}

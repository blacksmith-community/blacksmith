package aws

import (
	"github.com/fivetwenty-io/graft/internal/utils/mfa"
)

// mfaPromptIO is the interactive-prompt IO mfaTokenProvider uses,
// package-level and swappable in tests (restore via t.Cleanup, since it
// is shared, unsynchronized package state) for a fake reader/writer/
// isTTY so MFA tests never touch a real terminal. Production code leaves
// it at mfa.DefaultPromptIO(): stdin for input, stderr for the prompt
// (stdout carries graft's own output and must never receive a prompt),
// golang.org/x/term for the TTY check.
var mfaPromptIO = mfa.DefaultPromptIO()

// mfaTokenProvider builds an stscreds.AssumeRoleOptions.TokenProvider-
// shaped func for a role assumption protected by the MFA device serial.
// envToken is the one-shot code already read once from envVarName (by
// GetTargetConfig for a target, or directly from AWS_MFA_TOKEN for the
// un-namespaced path) - see mfa.TokenProvider's doc comment for the full
// three-branch resolution order (env token, interactive stderr prompt,
// clear error naming envVarName) this delegates to.
func mfaTokenProvider(serial, envToken, envVarName string) func() (string, error) {
	return mfa.TokenProvider(serial, envToken, envVarName, mfaPromptIO)
}

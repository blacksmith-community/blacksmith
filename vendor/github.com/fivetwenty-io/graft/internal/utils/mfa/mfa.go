// Package mfa builds stscreds-compatible MFA token providers for AWS
// role assumption. It is shared by internal/backends/aws (the
// environment-configured "AWS_{TARGET}_*" path) and pkg/graft (WithAWS/
// WithAWSTarget's AWSConfig.MFASerial), which cannot import
// internal/backends/aws - see pkg/graft/backend_aws.go's
// awsSSMAPI/awsSecretsAPI/awsSTSAPI doc comment for the import-cycle
// reason. Both callers get the same three-branch resolution: a one-shot
// environment token, an interactive stderr prompt when stdin is a
// terminal, or a clear error naming the environment variable to set.
package mfa

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"
)

// PromptIO holds the reader, writer, and TTY-detection function an
// interactive MFA prompt uses. Tests inject a fake In/Out/IsTTY to
// exercise every branch (env token, TTY prompt, no-TTY error) without a
// real terminal; production code uses DefaultPromptIO.
type PromptIO struct {
	// In is read for the operator's typed MFA code once a prompt has
	// been written to Out.
	In io.Reader
	// Out receives the prompt text. Production code MUST use stderr,
	// never stdout: `graft merge` writes its merged YAML document to
	// stdout, and a prompt sharing that stream would corrupt it.
	Out io.Writer
	// IsTTY reports whether In is an interactive terminal. A nil IsTTY
	// is treated as "not a terminal" (the no-TTY error branch), the same
	// as IsTTY returning false.
	IsTTY func() bool
}

// DefaultPromptIO returns the production PromptIO: stdin for input,
// stderr for the prompt, and golang.org/x/term's terminal detection
// (checked against stdin's file descriptor) for the TTY check.
func DefaultPromptIO() PromptIO {
	return PromptIO{
		In:  os.Stdin,
		Out: os.Stderr,
		IsTTY: func() bool {
			return term.IsTerminal(int(os.Stdin.Fd()))
		},
	}
}

// promptMu serializes concurrent interactive prompts that share stdin/
// stderr: two goroutines prompting at once would interleave their output
// and race reading the single answer line. stscreds.StdinTokenProvider
// documents the identical hazard for its own (stdout-writing) prompt.
var promptMu sync.Mutex

// TokenProvider returns an stscreds.AssumeRoleOptions.TokenProvider-
// shaped func (func() (string, error)) for a role assumption protected by
// the MFA device named serial. Each call to the returned func resolves a
// code in this order:
//
//  1. envToken, if non-empty, is returned exactly once. envToken is
//     expected to have been read from the environment a single time by
//     the caller before TokenProvider was called (not re-read on each
//     invocation of the returned func, since a TOTP code is only valid
//     once) - a second invocation of the returned func in this branch
//     errors instead of replaying the stale code.
//  2. Otherwise, when pio.IsTTY is non-nil and reports true, the func
//     writes "Enter MFA code for <serial>: " to pio.Out and reads one
//     line from pio.In, trimming leading/trailing whitespace.
//  3. Otherwise the func returns an error naming envVarName as the
//     variable to set.
//
// The returned func is itself safe for concurrent use (its own state -
// whether the one-shot envToken has been consumed - is protected by a
// private mutex), and the interactive-prompt branch additionally
// serializes against every other concurrent prompt via promptMu.
func TokenProvider(serial, envToken, envVarName string, pio PromptIO) func() (string, error) {
	var (
		mu   sync.Mutex
		used bool
	)

	return func() (string, error) {
		mu.Lock()
		defer mu.Unlock()

		if envToken != "" {
			if used {
				return "", fmt.Errorf("mfa: token for serial %q from %s was already used; a one-time code cannot be reused - set %s again with a fresh code, or run interactively", serial, envVarName, envVarName)
			}
			used = true
			return envToken, nil
		}

		if pio.IsTTY != nil && pio.IsTTY() {
			return promptForToken(serial, pio)
		}

		return "", fmt.Errorf("mfa: serial %q requires a token: set %s, or run graft interactively so it can prompt", serial, envVarName)
	}
}

// promptForToken writes the prompt to pio.Out and reads one line from
// pio.In, serialized against concurrent prompts via promptMu.
func promptForToken(serial string, pio PromptIO) (string, error) {
	promptMu.Lock()
	defer promptMu.Unlock()

	if _, err := fmt.Fprintf(pio.Out, "Enter MFA code for %s: ", serial); err != nil {
		return "", fmt.Errorf("mfa: writing prompt for serial %q: %w", serial, err)
	}

	line, err := bufio.NewReader(pio.In).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("mfa: reading token for serial %q: %w", serial, err)
	}

	token := strings.TrimSpace(line)
	if token == "" {
		return "", fmt.Errorf("mfa: no token entered for serial %q", serial)
	}
	return token, nil
}

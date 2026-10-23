package upgrade

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/upgrade/certscripts"
)

// The methods a TargetResult records.
const (
	verifyMethodProbe   = "probe"
	verifyMethodOpenSSL = "openssl"
	verifyMethodNone    = "none"
)

// verifyScript is the operation, and the script, that reads a listener's
// leaf on the VM.
const verifyScript = "verify"

// unverifiedDetail is the detail of a consumer that has no listener.
const unverifiedDetail = "applied, not externally verified"

// serverNamePattern is the shape of a server name verify.sh accepts. It
// starts with a letter or digit, so openssl never reads it as an option,
// and it holds no wildcard, which no client sends.
var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$`)

// fingerprintPattern is the shape of a fingerprint verify.sh prints.
var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// verifyTargets checks that every listener the journal names for the VM
// serves the new certificate of its variable. It probes each listener
// from the broker, sending the target's ServerName for SNI, and when the
// broker cannot reach the listener it runs verify.sh on the VM through
// run, which reads the listener over 127.0.0.1. newFingerprint maps each
// variable to the lowercase hex SHA-256 of its new leaf's DER.
//
// The caller sets each target's ServerName from the new certificate it
// already validated, so verification never reads CredHub, whose version
// answer carries the private key. A target with no name, or a name
// verify.sh would refuse, gets no SNI, and the fingerprint still decides
// the result.
//
// A variable the journal's files hold but no target names gets a result
// with method none, which passes, because nothing outside the VM can see
// it. Results keep the order of the targets, then the order of the files.
//
// verifyTargets only reads, so a resume can call it again. It reads the
// instance and the journal under mu, so the caller must not hold mu.
func (m *Manager) verifyTargets(ctx context.Context, inst *InstanceUpgrade, journal *VMJournal, newFingerprint map[string]string, run func(op string, args ...string) (ssh.Result, error)) []TargetResult {
	m.mu.RLock()

	deployment := inst.DeploymentName
	cid := journal.CID
	targets := slices.Clone(journal.Targets)
	files := slices.Clone(journal.Files)

	m.mu.RUnlock()

	check := targetCheck{
		manager: m,
		address: m.vmAddress(ctx, deployment, cid),
		run:     run,
	}

	var results []TargetResult

	listened := make(map[string]bool)

	for _, target := range targets {
		if target.Port <= 0 {
			continue
		}

		listened[target.Variable] = true
		results = append(results, check.verify(ctx, target, newFingerprint[target.Variable]))
	}

	for _, file := range files {
		if file.Variable == "" || listened[file.Variable] {
			continue
		}

		listened[file.Variable] = true
		results = append(results, TargetResult{Variable: file.Variable, Method: verifyMethodNone, OK: true, Detail: unverifiedDetail})
	}

	return results
}

// vmAddress returns the first address the director lists for the VM with
// the CID, or "" when it lists none, so every check falls back to the VM.
func (m *Manager) vmAddress(ctx context.Context, deployment, cid string) string {
	vms, err := directorCall(ctx, func(c context.Context) ([]bosh.VM, error) {
		return m.director.GetDeploymentVMs(c, deployment)
	})
	if err != nil || cid == "" {
		return ""
	}

	for _, vm := range vms {
		if vm.CID == cid && len(vm.IPs) > 0 {
			return vm.IPs[0]
		}
	}

	return ""
}

// targetCheck holds what every target of one VM shares.
type targetCheck struct {
	manager *Manager
	address string
	run     func(op string, args ...string) (ssh.Result, error)
}

// validServerName returns name when verify.sh accepts it, or "".
func validServerName(name string) string {
	if !serverNamePattern.MatchString(name) {
		return ""
	}

	return name
}

// verify checks one listener, from the broker first and from the VM when
// the broker cannot reach it.
func (c *targetCheck) verify(ctx context.Context, target TargetResult, want string) TargetResult {
	name := validServerName(target.ServerName)
	result := TargetResult{Variable: target.Variable, Port: target.Port, ServerName: target.ServerName}

	reached := false

	if c.address != "" {
		addr := net.JoinHostPort(c.address, strconv.Itoa(target.Port))

		leaf, _, err := c.manager.certDeps().Prober.Leaf(ctx, addr, name)
		if err == nil && leaf != nil {
			result.Method = verifyMethodProbe
			result.Fingerprint = certinventory.Fingerprint(leaf)
			reached = true
		}
	}

	if !reached {
		result.Method = verifyMethodOpenSSL

		fingerprint, detail := c.readOnVM(target.Port, name)
		if fingerprint == "" {
			result.Detail = detail

			return result
		}

		result.Fingerprint = fingerprint
	}

	switch {
	case want == "":
		result.Detail = "No new fingerprint is known for this variable, so the listener's certificate can't be compared."
	case strings.EqualFold(result.Fingerprint, want):
		result.OK = true
		result.Detail = "The listener serves the new certificate."
	default:
		result.Detail = "The listener serves a different certificate from the new one."
	}

	return result
}

// readOnVM runs verify.sh for the port and returns the fingerprint it
// printed, or "" and the reason it has none. The reason never carries
// the runner's error or the script's output.
func (c *targetCheck) readOnVM(port int, name string) (string, string) {
	if c.run == nil {
		return "", "The broker could not reach the listener, and there is no SSH session to check it on the VM."
	}

	out, err := c.run(verifyScript, strconv.Itoa(port), name)

	switch {
	case err != nil:
		return "", "The broker could not reach the listener, and the check on the VM did not finish."
	case out.ExitCode != 0:
		return "", fmt.Sprintf("The broker could not reach the listener, and openssl on the VM read no certificate from it (exit status %d).", out.ExitCode)
	case out.Truncated:
		return "", "The broker could not reach the listener, and the check on the VM printed more than it should."
	}

	lines, err := certscripts.ParseLines(out.Stdout, map[string]bool{"fingerprint": true})
	if err != nil || len(lines) != 1 || !fingerprintPattern.MatchString(lines[0]["fingerprint"]) {
		return "", "The broker could not reach the listener, and the check on the VM printed no single fingerprint."
	}

	return lines[0]["fingerprint"], ""
}

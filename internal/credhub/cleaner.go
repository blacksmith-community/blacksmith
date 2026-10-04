package credhub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

const (
	// RunTimeout bounds one cleanup run from start to finish.
	RunTimeout = 2 * time.Minute
	// maxAttempts is how many times a transient failure is tried.
	maxAttempts = 3
	opCleanup   = "cleanup"
)

// ErrDeploymentNotProvenGone means the director did not prove the deployment
// is gone, so the cleaner deleted nothing.
var ErrDeploymentNotProvenGone = errors.New("deployment is not proven gone")

// finderDeleter is the part of Client the cleaner uses.
type finderDeleter interface {
	FindByPath(ctx context.Context, path string) ([]string, error)
	Delete(ctx context.Context, name string) error
}

// directorProber is the part of bosh.Director the cleaner uses.
type directorProber interface {
	GetInfo() (*bosh.Info, error)
	GetDeployment(name string) (*bosh.DeploymentDetail, error)
	FindRunningTaskForDeployment(deploymentName string) (*bosh.Task, error)
}

// Failure records one name, or the deployment prefix when listing failed,
// that the cleaner could not delete.
type Failure struct {
	Name string
	Op   string
	Err  error
}

// Result records what one cleanup run did.
type Result struct {
	Target Target
	// Prefix is /<director>/<deployment>/ once the guard has passed.
	Prefix string
	// Deleted holds the names deleted, including names CredHub reported as
	// already gone.
	Deleted []string
	// Skipped holds listed names the cleaner does not own.
	Skipped []string
	// Failed holds the names, or the prefix, that could not be cleaned.
	Failed []Failure
	// Refused is set when the guard or the proof of absence refused the
	// run, which then deleted nothing.
	Refused error
	// Duplicate is set when another run for the same deployment was
	// already in flight, so this one did nothing.
	Duplicate bool
}

// Cleaner deletes the director CredHub variables of a deployment the
// director has proven gone.
type Cleaner struct {
	client   finderDeleter
	director directorProber
	policy   func() Policy
	log      logger.Logger
	backoff  []time.Duration
	inFlight sync.Map
}

// NewCleaner builds a cleaner. policy is called on every run so the catalog's
// plan IDs are always current. The cleaner fills Policy.InfoDirectorName from
// a fresh /info on every run and ignores any value policy returns there.
func NewCleaner(client finderDeleter, director directorProber, policy func() Policy, log logger.Logger) *Cleaner {
	return &Cleaner{
		client:   client,
		director: director,
		policy:   policy,
		log:      log,
		backoff:  []time.Duration{1 * time.Second, 3 * time.Second},
	}
}

// CleanupDeployment runs one cleanup for target. It proves the deployment is
// gone itself rather than trusting its caller, deletes only names the guard
// allows, and logs every outcome. It never panics on a CredHub or director
// failure, and at most one run per deployment is in flight at a time.
func (c *Cleaner) CleanupDeployment(ctx context.Context, target Target) Result {
	result := Result{Target: target}

	if _, running := c.inFlight.LoadOrStore(target.DeploymentName, struct{}{}); running {
		c.log.Infof("CredHub cleanup for deployment %s (instance %s) is already running, so this request does nothing", target.DeploymentName, target.InstanceID)

		result.Duplicate = true

		return result
	}
	defer c.inFlight.Delete(target.DeploymentName)

	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()

	policy := c.policy()

	if !c.applyGuard(target, policy, &result) || !c.proveAbsent(ctx, target, &result) {
		return result
	}

	prefix, confirmed := c.confirmDirector(target, policy, &result)
	if !confirmed {
		return result
	}

	result.Prefix = prefix

	owned, listed := c.list(ctx, target, prefix, policy, &result)
	if !listed {
		return result
	}

	for _, name := range owned {
		c.deleteOne(ctx, target, name, &result)
	}

	c.log.Infof("CredHub cleanup for deployment %s (instance %s) deleted %d of %d owned credentials under %s, skipped %d listed names it does not own, and failed on %d",
		target.DeploymentName, target.InstanceID, len(result.Deleted), len(owned), prefix, len(result.Skipped), len(result.Failed))

	return result
}

// applyGuard checks every guard rule that needs no I/O. The /info name is
// not known yet, so rule 1 is checked against the configured name here and
// against a fresh /info in confirmDirector.
func (c *Cleaner) applyGuard(target Target, policy Policy, result *Result) bool {
	precheck := policy
	precheck.InfoDirectorName = policy.DirectorName

	_, err := DeploymentPrefix(target, precheck)
	if err == nil {
		return true
	}

	c.refuse(target, policy, err, result)

	return false
}

// proveAbsent requires the director to answer its own not-found for the
// deployment and to have no task running on it.
func (c *Cleaner) proveAbsent(ctx context.Context, target Target, result *Result) bool {
	if !c.stillRunning(ctx, target, result) {
		return false
	}

	reason := ""

	_, err := c.director.GetDeployment(target.DeploymentName)

	switch {
	case err == nil:
		reason = "the director still has deployment " + target.DeploymentName
	case !errors.Is(err, bosh.ErrDeploymentNotFound):
		reason = fmt.Sprintf("the director could not confirm deployment %s is gone: %v", target.DeploymentName, err)
	default:
		task, taskErr := c.director.FindRunningTaskForDeployment(target.DeploymentName)

		switch {
		case taskErr != nil:
			reason = fmt.Sprintf("the director could not list the tasks of deployment %s: %v", target.DeploymentName, taskErr)
		case task != nil:
			reason = fmt.Sprintf("task %d (%s) is running on deployment %s", task.ID, task.State, target.DeploymentName)
		}
	}

	if reason == "" {
		return true
	}

	result.Refused = fmt.Errorf("%w: %s", ErrDeploymentNotProvenGone, reason)
	c.log.Warnf("CredHub cleanup left the credentials of deployment %s (instance %s) in place and deleted nothing because %s, and the deprovision itself succeeded. To investigate, check the director's view of the deployment and its tasks.",
		target.DeploymentName, target.InstanceID, reason)

	return false
}

// confirmDirector reads the director's name from a fresh /info and builds the
// prefix only when it equals the configured name.
func (c *Cleaner) confirmDirector(target Target, policy Policy, result *Result) (string, bool) {
	info, err := c.director.GetInfo()
	if err != nil {
		c.refuse(target, policy, fmt.Errorf("%w: reading the director's /info failed: %w", ErrDirectorNameMismatch, err), result)

		return "", false
	}

	policy.InfoDirectorName = info.Name

	prefix, err := DeploymentPrefix(target, policy)
	if err != nil {
		c.refuse(target, policy, err, result)

		return "", false
	}

	return prefix, true
}

// list finds the names under prefix, retrying transient failures, and keeps
// the owned ones.
func (c *Cleaner) list(ctx context.Context, target Target, prefix string, policy Policy, result *Result) ([]string, bool) {
	var names []string

	err := c.withRetries(ctx, func() error {
		var findErr error

		names, findErr = c.client.FindByPath(ctx, prefix)

		return findErr //nolint:wrapcheck // the client's errors already name the operation
	})
	if err != nil {
		result.Failed = append(result.Failed, Failure{Name: prefix, Op: opFind, Err: err})
		causes, check := explainFailure(opFind, err)
		c.log.Errorf("CredHub cleanup could not list the credentials under %s for deployment %s (instance %s), and the deprovision itself succeeded. The error was %v. The likely cause is that %s. To investigate, check %s. To clean up by hand, run credhub find -p %s and then credhub delete -n <name> for each name it lists.",
			prefix, target.DeploymentName, target.InstanceID, err, causes, check, prefix)

		return nil, false
	}

	owned, skipped := FilterOwned(prefix, names, policy)
	result.Skipped = skipped

	c.log.Infof("CredHub cleanup found %d credentials under %s for deployment %s (instance %s) and owns %d of them: %s",
		len(names), prefix, target.DeploymentName, target.InstanceID, len(owned), strings.Join(owned, ", "))

	if len(skipped) > 0 {
		c.log.Warnf("CredHub cleanup skipped %d listed names under %s that are not a single variable of deployment %s, and left them in place: %s",
			len(skipped), prefix, target.DeploymentName, strings.Join(skipped, ", "))
	}

	return owned, true
}

func (c *Cleaner) deleteOne(ctx context.Context, target Target, name string, result *Result) {
	err := c.withRetries(ctx, func() error { return c.client.Delete(ctx, name) })

	switch {
	case err == nil:
		result.Deleted = append(result.Deleted, name)
		c.log.Infof("CredHub cleanup deleted %s for deployment %s (instance %s)", name, target.DeploymentName, target.InstanceID)
	case errors.Is(err, ErrNotFound):
		result.Deleted = append(result.Deleted, name)
		c.log.Infof("CredHub cleanup found %s already gone for deployment %s (instance %s)", name, target.DeploymentName, target.InstanceID)
	default:
		result.Failed = append(result.Failed, Failure{Name: name, Op: opDelete, Err: err})
		causes, check := explainFailure(opDelete, err)
		c.log.Errorf("CredHub cleanup could not delete %s for deployment %s (instance %s), and the deprovision itself succeeded. The error was %v. The likely cause is that %s. To investigate, check %s. To remove it by hand, run credhub delete -n %s",
			name, target.DeploymentName, target.InstanceID, err, causes, check, name)
	}
}

// refuse records and logs a guard refusal. A protected deployment's line
// never suggests deleting anything by hand.
func (c *Cleaner) refuse(target Target, policy Policy, err error, result *Result) {
	result.Refused = err

	var hint string

	switch {
	case errors.Is(err, ErrProtectedDeployment):
		hint = " The credentials of a protected deployment must stay where they are."
	case target.DeploymentName == "" || strings.ContainsAny(target.DeploymentName, forbiddenNameChars):
		hint = " Look at the deployment name before cleaning anything by hand."
	default:
		hint = " If these variables should go, find them with credhub find -p /" + policy.DirectorName + "/" + target.DeploymentName +
			" and remove each with credhub delete -n <name>."
	}

	c.log.Warnf("CredHub cleanup refused deployment %q (instance %q) and deleted nothing because %v, and the deprovision itself succeeded. A refusal is the guard doing its job.%s",
		target.DeploymentName, target.InstanceID, err, hint)
}

// stillRunning reports whether the run's context is still live, and records
// a failure when it is not.
func (c *Cleaner) stillRunning(ctx context.Context, target Target, result *Result) bool {
	err := ctx.Err()
	if err == nil {
		return true
	}

	result.Failed = append(result.Failed, Failure{Name: target.DeploymentName, Op: opCleanup, Err: err})
	c.log.Errorf("CredHub cleanup for deployment %s (instance %s) stopped before it deleted anything because %v, and the deprovision itself succeeded. The run has a %s deadline, so a slow director or CredHub is the likely cause.",
		target.DeploymentName, target.InstanceID, err, RunTimeout)

	return false
}

// withRetries tries call up to maxAttempts times while it fails with a
// transient error, waiting between attempts.
func (c *Cleaner) withRetries(ctx context.Context, call func() error) error {
	var err error

	for attempt := range maxAttempts {
		err = call()
		if err == nil || !isTransient(err) || attempt == maxAttempts-1 {
			return err
		}

		wait := c.backoff[min(attempt, len(c.backoff)-1)]

		timer := time.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("%w (stopped retrying: %w)", err, ctx.Err())
		case <-timer.C:
		}
	}

	return err
}

// isTransient reports whether a failure is worth another attempt: network
// errors, 429, and 5xx answers. TLS failures, 401, 403, and the run's own
// deadline are not.
func isTransient(err error) bool {
	if errors.Is(err, context.Canceled) || isTLSFailure(err) {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= http.StatusInternalServerError
	}

	var uaaErr *UAAError
	if errors.As(err, &uaaErr) {
		return uaaErr.Status == http.StatusTooManyRequests || uaaErr.Status >= http.StatusInternalServerError
	}

	if errors.Is(err, ErrUAAUnreachable) {
		return true
	}

	var netErr net.Error

	var urlErr *url.Error

	return errors.As(err, &netErr) || errors.As(err, &urlErr)
}

func isTLSFailure(err error) bool {
	var verifyErr *tls.CertificateVerificationError

	var unknownAuthority x509.UnknownAuthorityError

	var hostnameErr x509.HostnameError

	var invalidErr x509.CertificateInvalidError

	if errors.As(err, &verifyErr) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) {
		return true
	}

	return strings.Contains(err.Error(), "x509:")
}

// explainFailure holds the failure table operators read. It returns the
// likely causes of a failed CredHub cleanup step and what to check.
func explainFailure(operation string, err error) (string, string) {
	var uaaErr *UAAError

	var apiErr *APIError

	switch {
	case errors.As(err, &uaaErr) && uaaErr.Status == http.StatusUnauthorized:
		return "the credhub.client_id client (blacksmith_credhub by default) does not exist on the director's UAA, or its secret does not match credhub.client_secret",
			"genesis @<env>:bosh do -- uaa clients get blacksmith_credhub, and the repair command in the Blacksmith kit manual"
	case errors.As(err, &uaaErr), errors.Is(err, ErrUAAUnreachable), errors.Is(err, ErrUAANoToken):
		return "the UAA URL from the director's /info (or credhub.uaa_url) is wrong, UAA is down, or bosh.cacert does not sign UAA's certificate",
			"the director's /info UAA URL and the broker's bosh.cacert"
	case errors.Is(err, ErrProtectedName), errors.Is(err, ErrEmptyName):
		return "the name is under a protected prefix or empty, and refusing it is the guard doing its job",
			"that the name belongs to a protected deployment, which means it was left in place on purpose"
	case errors.As(err, &apiErr):
		return explainStatus(operation, apiErr.Status)
	case isTLSFailure(err):
		return "credhub.ca_cert does not match the CA or certificate CredHub presents, which happens after credhub_tls rotates",
			"the bosh exodus credhub_ca_cert against the certificate CredHub presents"
	case errors.Is(err, ErrBadFindAnswer):
		return "something other than CredHub answered at credhub.url",
			"credhub.url"
	case isUnreachable(err):
		return "credhub.url is wrong, or CredHub on the director is down",
			"credhub.url, and monit summary on the director"
	default:
		return fmt.Sprintf("the CredHub %s request failed in an unexpected way", operation),
			"the broker log around this line, and credhub.url"
	}
}

func explainStatus(operation string, status int) (string, string) {
	switch {
	case status == http.StatusUnauthorized:
		return "the token came from a UAA CredHub does not trust, the UAA signing key rotated and CredHub has not picked it up, or the client was deleted between the token fetch and the request",
			"credhub.uaa_url if it is set, the director's UAA, and CredHub's uaa settings on the director"
	case status == http.StatusForbidden:
		return "the credhub.client_id client lacks credhub.read or credhub.write (CredHub needs both for every call), or someone turned CredHub ACLs on without a permission for uaa-client:blacksmith_credhub",
			"the client's authorities, and credhub curl -p '/api/v2/permissions?path=<prefix>*&actor=uaa-client:blacksmith_credhub'"
	case status == http.StatusTooManyRequests:
		return "CredHub is rate limiting requests",
			"CredHub load and the CredHub logs on the director"
	case status >= http.StatusInternalServerError:
		return "CredHub or its database is unhealthy",
			"the CredHub logs on the director"
	default:
		return fmt.Sprintf("CredHub answered the %s request with an unexpected status", operation),
			"the CredHub logs on the director, and credhub.url"
	}
}

func isUnreachable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error

	var urlErr *url.Error

	if errors.As(err, &netErr) || errors.As(err, &urlErr) {
		return true
	}

	text := err.Error()

	return strings.Contains(text, "connection refused") || strings.Contains(text, "no such host") || strings.Contains(text, "i/o timeout")
}

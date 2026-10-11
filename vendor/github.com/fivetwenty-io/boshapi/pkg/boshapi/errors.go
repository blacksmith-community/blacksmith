package boshapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is an error answer from the BOSH Director. It records the HTTP
// status, the request that drew the answer, and the director's own code and
// description, so a caller can say what failed without parsing text.
type APIError struct {
	// StatusCode is the HTTP status the director answered with.
	StatusCode int `json:"-" yaml:"-"`
	// Method is the HTTP method of the request.
	Method string `json:"-" yaml:"-"`
	// Path is the request path, without the query string.
	Path string `json:"-" yaml:"-"`
	// Code is the director's numeric error code, or 0 when the body was not
	// the director's JSON error shape.
	Code int `json:"code" yaml:"code"`
	// Description is the director's description. When the body was not the
	// director's JSON error shape, it holds the body, trimmed and cut to
	// 1 KiB, or 64 KiB for the JSON body of a diff route. A body that was cut
	// ends in "…(truncated)".
	Description string `json:"description" yaml:"description"`
	// Mutation is true when the request was not safe to resend: a POST,
	// PUT, PATCH, or DELETE, or a GET that starts a director task.
	Mutation bool `json:"-" yaml:"-"`
}

// Error implements the error interface. It leaves out the code when the
// director sent none, and the description when it is empty, so the message
// never ends in a dangling colon.
func (e *APIError) Error() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "BOSH director answered %s %s with HTTP %d", e.Method, e.Path, e.StatusCode)

	if e.Code != 0 {
		fmt.Fprintf(&msg, " (code %d)", e.Code)
	}

	if e.Description != "" {
		fmt.Fprintf(&msg, ": %s", e.Description)
	}

	return msg.String()
}

// TransportError reports a request that got no answer from the director:
// the connection failed, timed out, or broke before a response arrived. It
// unwraps to Err, so errors.Is(err, context.DeadlineExceeded) still works.
type TransportError struct {
	// Method is the HTTP method of the request.
	Method string
	// Path is the request path, without the query string.
	Path string
	// ContextID is the X-Bosh-Context-Id the request carried, if any.
	ContextID string
	// Sent is true when the request headers were handed to the connection
	// on some attempt, so the director may have received the request. It is
	// set before the headers are flushed, so it is also true when the flush
	// failed and nothing reached the director. False means the director
	// cannot have received the request.
	Sent bool
	// BodyCutShort is true when a streamed body, such as a tarball upload,
	// stopped because its reader ended or failed before the size the caller
	// gave. The director never got a whole request, so it cannot have
	// accepted it. Err wraps the cause, io.ErrUnexpectedEOF or the reader's
	// own error. IsOutcomeUnknown is false for such an error.
	BodyCutShort bool
	// Timeout is true when the HTTP client's timeout or a transport
	// deadline fired. It is false when the caller's own context ended.
	Timeout bool
	// Mutation is true when the request was not safe to resend: a POST,
	// PUT, PATCH, or DELETE, or a GET that starts a director task.
	Mutation bool
	// Attempts is how many times the request was tried.
	Attempts int
	// ContextErr is the caller's context error, context.Canceled or
	// context.DeadlineExceeded, when the caller stopped waiting before an
	// answer came. It is nil otherwise.
	ContextErr error
	// LastAnswer is the director's answer to an earlier attempt that was
	// retried, such as a 503 while the director restarts, with its body cut
	// to 1 KiB. It is nil when no attempt drew an answer.
	LastAnswer *APIError
	// Err is the underlying error. It names the director URL.
	Err error
}

// Error implements the error interface.
func (e *TransportError) Error() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "%s %s to the BOSH director got no answer", e.Method, e.Path)

	if e.Attempts > 1 {
		fmt.Fprintf(&msg, " after %d attempts", e.Attempts)
	}

	if e.ContextID != "" {
		fmt.Fprintf(&msg, " (context ID %s)", e.ContextID)
	}

	fmt.Fprintf(&msg, ": %v. ", e.Err)

	if e.LastAnswer != nil {
		fmt.Fprintf(&msg, "The director last answered HTTP %d", e.LastAnswer.StatusCode)

		if e.LastAnswer.Code != 0 {
			fmt.Fprintf(&msg, " (code %d)", e.LastAnswer.Code)
		}

		if e.LastAnswer.Description != "" {
			fmt.Fprintf(&msg, ": %s", e.LastAnswer.Description)
		}

		msg.WriteString(". ")
	}

	switch {
	case e.BodyCutShort:
		msg.WriteString("The upload was cut short because the reader that supplies the body ended or failed " +
			"before it gave all the bytes of the size, so the director cannot have accepted it, and it was " +
			"not resent. Check the file or stream you are uploading, and send the upload again.")
	case e.Sent && e.Mutation:
		msg.WriteString("The request may have reached the director, so the director may have accepted it, " +
			"and it was not resent. ")

		if e.ContextID != "" {
			fmt.Fprintf(&msg, "Look for a director task with context ID %s before you send it again.", e.ContextID)
		} else {
			msg.WriteString("Look for a recent director task for the same deployment before you send it again.")
		}
	case e.ContextErr != nil:
		e.writeCallerStopped(&msg)
	case e.Timeout:
		msg.WriteString("The director did not answer in time. The likely causes are a director that is " +
			"overloaded or restarting, or a network path that drops traffic. Check that the director " +
			"answers GET /info, and check the network path between this client and the director.")
	case !e.Sent:
		msg.WriteString("The request never reached the director. Check the director URL, that the " +
			"director is running, and the network path and TLS trust between this client and the director.")
	default:
		msg.WriteString("The connection broke after the request was handed to it, so the director may have " +
			"received the request. Check that the director is running, and check the network path " +
			"between this client and the director.")
	}

	return msg.String()
}

// writeCallerStopped explains a request that ended because the caller's
// context ended, naming the deadline or the cancel.
func (e *TransportError) writeCallerStopped(msg *strings.Builder) {
	deadline := errors.Is(e.ContextErr, context.DeadlineExceeded)

	if deadline {
		msg.WriteString("The caller stopped waiting when its context deadline passed")
	} else {
		msg.WriteString("The caller stopped waiting when its context was cancelled")
	}

	msg.WriteString(", so the client gave up on the request")

	if !e.Sent {
		msg.WriteString(" before it reached the director")
	}

	msg.WriteString(". ")

	if deadline {
		msg.WriteString("If the deadline is too short for this call, give it a longer one, and check that " +
			"the director answers GET /info and is not overloaded or restarting.")
	} else {
		msg.WriteString("Check what cancelled the context, such as a shutdown of the calling process.")
	}
}

// Unwrap returns the underlying error.
func (e *TransportError) Unwrap() error {
	return e.Err
}

// RedirectError reports a redirect answer that the client does not follow.
// The client never follows a redirect, because resending a request body to
// another place could repeat a change. A 302 to a request that starts a
// director task is the director's normal answer, and its Location ends in
// /tasks/<id>. Any other redirect, or a 302 whose Location holds no task ID,
// comes back as this error.
type RedirectError struct {
	// Method is the HTTP method of the request.
	Method string
	// Path is the request path, without the query string.
	Path string
	// Status is the HTTP status of the redirect.
	Status int
	// Location is the Location header the director sent, or empty when it
	// sent none.
	Location string
	// DirectorURL is the director URL the client sent the request to.
	DirectorURL string
	// ContextID is the context ID the request carried, or empty when it
	// carried none. Tasks the request started can be found by it.
	ContextID string
	// Mutation is true when the request was not safe to resend: a POST,
	// PUT, PATCH, or DELETE, or a GET that starts a director task. The
	// director may have acted on it before it redirected.
	Mutation bool
}

// Error implements the error interface.
func (e *RedirectError) Error() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "BOSH director at %s answered %s %s with HTTP %d", e.DirectorURL, e.Method, e.Path, e.Status)

	if e.Location != "" {
		fmt.Fprintf(&msg, " and a redirect to %q", e.Location)
	} else {
		msg.WriteString(" and no Location header")
	}

	msg.WriteString(". The client does not follow redirects")

	if !e.Mutation {
		msg.WriteString(", and this request is not a task-creating call, so it expects an answer from the director itself. " +
			"Check the director URL, and check for a proxy, a single sign-on gateway, or a rewrite rule in front of the director.")

		return msg.String()
	}

	msg.WriteString(", and a redirect that does not end in /tasks/<id> gives no task ID to read. " +
		"The director may have started a task or applied the change, and the request was not resent. ")

	if e.ContextID != "" {
		fmt.Fprintf(&msg, "Look for a director task with context ID %s before you send it again. ", e.ContextID)
	} else {
		msg.WriteString("Look at the recent tasks on the director for one that matches this request before you send it again. ")
	}

	msg.WriteString("Check the director URL, and check for a proxy, a single sign-on gateway, or a rewrite rule in front of the director.")

	return msg.String()
}

// TaskAnswerError reports that a request which starts a director task drew an
// answer that is not the director's redirect to /tasks/<id>, such as a 200
// with some other body. The director may have started a task, and the client
// has no task ID to hand back.
type TaskAnswerError struct {
	// Method is the HTTP method of the request.
	Method string
	// Path is the request path, without the query string.
	Path string
	// Status is the HTTP status of the answer.
	Status int
	// Body is the start of the answer's body, cut to 1 KiB.
	Body string
	// DirectorURL is the director URL the client sent the request to.
	DirectorURL string
	// ContextID is the context ID the request carried, or empty when it
	// carried none. A task the request started can be found by it.
	ContextID string
}

// Error implements the error interface.
func (e *TaskAnswerError) Error() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "BOSH director at %s answered %s %s with HTTP %d, but a request that starts a task "+
		"is answered with a redirect to /tasks/<id>", e.DirectorURL, e.Method, e.Path, e.Status)

	if e.Body != "" {
		fmt.Fprintf(&msg, ". The body was %q", e.Body)
	} else {
		msg.WriteString(". The body was empty")
	}

	msg.WriteString(". The director may have started a task, and the request was not resent. " +
		"Check the director URL, and check for a proxy in front of the director that answers in its place. ")

	if e.ContextID != "" {
		fmt.Fprintf(&msg, "Look for a director task with context ID %s before you send it again.", e.ContextID)
	} else {
		msg.WriteString("Look at the recent tasks on the director for one that matches this request before you send it again.")
	}

	return msg.String()
}

// IsOutcomeUnknown reports whether a request that is not safe to resend may
// have reached the director even though no usable answer came back, so the
// director may have acted on it. That is the case for a TransportError on a
// mutation whose request may have reached the director, unless the error says
// the body was cut short, because the director cannot accept a body that ended
// early. It is also the case for an APIError with status 502 or 504 on a
// mutation, because a gateway answered and the director may already have
// accepted the request. A RedirectError on a mutation and a TaskAnswerError
// are also outcome-unknown, because the director answered a request that
// starts a task with something that holds no task ID. Such a request is never
// resent automatically.
func IsOutcomeUnknown(err error) bool {
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		return transportErr.Mutation && transportErr.Sent && !transportErr.BodyCutShort
	}

	var redirectErr *RedirectError
	if errors.As(err, &redirectErr) {
		return redirectErr.Mutation
	}

	var answerErr *TaskAnswerError
	if errors.As(err, &answerErr) {
		return true
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Mutation &&
			(apiErr.StatusCode == http.StatusBadGateway || apiErr.StatusCode == http.StatusGatewayTimeout)
	}

	return false
}

// Director error codes that the library reads. The director defines each code
// in its error table and pairs it with an HTTP status, and a code means
// nothing apart from that table. For instance, 10000 is TaskNotFound and 60000
// is PackageInvalidArchive.
const (
	ErrorCodeTaskNotFound       = 10000
	ErrorCodeUserNotFound       = 20000
	ErrorCodeDeploymentNotFound = 70000
	ErrorCodeInstanceNotFound   = 70001

	// ErrorCodeBadConfigRequest is the answer to a config request that
	// leaves out a required field, such as its type or its name.
	ErrorCodeBadConfigRequest = 440010

	// ErrorCodeUnauthorizedToAccessDeployment comes with a 401 to a caller
	// whose token is valid but lacks the scope the route needs. The director
	// answers a missing or invalid token with a plain 401 and no code.
	ErrorCodeUnauthorizedToAccessDeployment = 600000
)

// Sentinel errors for common client-side conditions.
var (
	// ErrNotFound matches, through errors.Is, an error for a resource the
	// director has no record of and that did not come as an HTTP answer, such
	// as ConfigNotFoundError.
	ErrNotFound = errors.New("not found")

	ErrConfigRequired           = errors.New("config is required")
	ErrDirectorURLRequired      = errors.New("director URL is required")
	ErrStaticTokenCannotRefresh = errors.New("static token cannot be refreshed")
	ErrNoUAAEndpoint            = errors.New("no UAA endpoint found in director info")

	// ErrRecreateVMsCreatedBeforeRequiresRecreate is returned when
	// DeployOptions.RecreateVMsCreatedBefore is set without Recreate.
	ErrRecreateVMsCreatedBeforeRequiresRecreate = errors.New("recreate_vms_created_before requires Recreate to be true")
	// ErrRecreateVMsCreatedBeforeRequiresRecreateState is returned when
	// UpdateStateOptions.RecreateVMsCreatedBefore is set with a state other
	// than "recreate".
	ErrRecreateVMsCreatedBeforeRequiresRecreateState = errors.New(`recreate_vms_created_before requires state "recreate"`)
	// ErrRecreateVMsCreatedBeforeJobLevelOnly is returned when
	// UpdateStateOptions.RecreateVMsCreatedBefore is used with a per-instance
	// state change; the director only accepts it on job-level state changes.
	ErrRecreateVMsCreatedBeforeJobLevelOnly = errors.New("recreate_vms_created_before is only supported for job-level state changes")
	// ErrDiskNameRequired is returned when a dynamic disk operation is
	// attempted with an empty disk name.
	ErrDiskNameRequired = errors.New("disk name is required")
	// ErrProvideRequestRequired is returned when Provide is called with a nil
	// request.
	ErrProvideRequestRequired = errors.New("provide dynamic disk request is required")
	// ErrInstanceIDRequired is returned when a dynamic disk operation that
	// targets an instance is given an empty instance ID.
	ErrInstanceIDRequired = errors.New("instance ID is required")
	// ErrDiskPoolNameRequired is returned when a dynamic disk operation is
	// given an empty disk pool name.
	ErrDiskPoolNameRequired = errors.New("disk pool name is required")
	// ErrDiskSizeInvalid is returned when a dynamic disk operation is given
	// a disk size below 1.
	ErrDiskSizeInvalid = errors.New("disk size must be at least 1")
	// ErrCreateRequestRequired is returned when Create is called with a nil
	// request.
	ErrCreateRequestRequired = errors.New("create dynamic disk request is required")
	// ErrAttachRequestRequired is returned when Attach is called with a nil
	// request.
	ErrAttachRequestRequired = errors.New("attach dynamic disk request is required")
	// ErrDeploymentNameRequired is returned when Create is called with an
	// empty deployment name.
	ErrDeploymentNameRequired = errors.New("deployment name is required")
	// ErrBlobstoreIDRequired is returned when Resources().Download or
	// Packages().DownloadBlob is called with an empty blobstore ID, before any
	// request is sent.
	ErrBlobstoreIDRequired = errors.New("blobstore ID is required")
	// ErrJobSelectorRequired is returned when Releases().GetJob is called with
	// an empty release name, job name, or fingerprint, before any request is
	// sent.
	ErrJobSelectorRequired = errors.New("release name, job name, and fingerprint are required")
	// ErrCancelSelectorRequired is returned when Tasks().CancelMatching is
	// called with a nil request, or one that sets no States, no Types, and no
	// Deployment, before any request is sent.
	ErrCancelSelectorRequired = errors.New("cancel request must select by state, type, or deployment")
	// ErrLogFilterInvalid is returned when Instances().FetchLogs is called
	// with a LogOptions.Filters entry that holds whitespace or a comma, before
	// any request is sent. The director splits the filters on both, so the
	// entry would arrive as two filters.
	ErrLogFilterInvalid = errors.New("log filter must not contain whitespace or a comma")
	// ErrEventFieldRequired is returned when Events().Create is called with a
	// nil request, or with an empty Action, ObjectType, or ObjectName, before
	// any request is sent.
	ErrEventFieldRequired = errors.New("event request needs an action, an object type, and an object name")
	// ErrAttachDiskOptionsInvalid is returned when Disks().AttachDisk is
	// called with nil options, or with an empty DiskCID, Deployment, Job, or
	// InstanceID, before any request is sent.
	ErrAttachDiskOptionsInvalid = errors.New("attach disk needs a disk CID, a deployment, a job, and an instance ID")
	// ErrResolutionsRequired is returned when CloudCheck().ResolveProblems is
	// called with a nil request, or one whose Resolutions is nil or empty,
	// before any request is sent. The director reads the resolutions with
	// keys, so a null value fails the task.
	ErrResolutionsRequired = errors.New("resolve problems needs at least one resolution")
	// ErrUploadFileInvalid is returned when Releases().UploadFile or
	// Stemcells().UploadFile is called without options, without a reader, or
	// with a size that is not above zero, before any request is sent.
	ErrUploadFileInvalid = errors.New("upload file needs a reader and a size above zero")
	// ErrDeploymentsRequired is returned when DeploymentConfigs().List is
	// called with nil options or an empty Deployments list, before any request
	// is sent. The director answers an empty list when it is given no
	// deployment name, so the call could only mislead.
	ErrDeploymentsRequired = errors.New("deployment configs need at least one deployment name")
	// ErrConfigIDInvalid is returned when Configs().Get or Configs().Delete is
	// called with an ID that is not a decimal integer, such as "", ".", "..",
	// or one with a slash, before any request is sent. The director's config
	// IDs are integers, and such an ID would reach another route: an empty one
	// reaches the list route on Get and the delete-by-type-and-name route on
	// Delete.
	ErrConfigIDInvalid = errors.New("config ID is not a decimal integer")
	// ErrUploadLocationRequired is returned when Releases().Upload or
	// Stemcells().Upload is called without options, or with an empty URL,
	// before any request is sent. The director reads the location of the
	// tarball from the request body and queues a task that fails when it is
	// missing. To upload a tarball from disk, use UploadFile.
	ErrUploadLocationRequired = errors.New("upload needs the URL of a tarball")
	// ErrExportRequestRequired is returned when ExportRelease is called with a
	// nil request, before anything is sent. The director reads the release and
	// stemcell from the request body and has nothing to export without them.
	ErrExportRequestRequired = errors.New("export release request is required")
	// ErrPathSegmentInvalid is returned, before anything is sent, when a name
	// or ID that goes into a request path is empty, ".", or "..". The director's
	// router and nginx normalise those, so the request would reach a different
	// route than the caller meant.
	ErrPathSegmentInvalid = errors.New("path segment is empty or a dot segment")
	// ErrAZRequired is returned when Create is called with an empty
	// availability zone.
	ErrAZRequired = errors.New("availability zone is required")
	// ErrInvalidCACert is returned when Config.CACert is neither PEM text
	// holding at least one certificate nor the path of a readable PEM file.
	ErrInvalidCACert = errors.New("invalid CACert")
	// ErrInvalidAuthConfig is returned when building a client whose Config
	// asks for an auth mode it cannot use, such as AuthDiscover without
	// ClientID and ClientSecret.
	ErrInvalidAuthConfig = errors.New("invalid auth configuration")
)

// ConfigNotFoundError reports that the director answered a request for one
// config, GET or DELETE /configs/<id>, with HTTP 404, which means it holds no
// config with the requested ID. It replaces the APIError for that one answer,
// as bosh-cli does with its "No config" message, and it unwraps to that
// APIError so errors.As still finds the status, code, and description.
type ConfigNotFoundError struct {
	ID string
	// Method is the HTTP method of the request the director answered, GET or
	// DELETE.
	Method string
	// Err is the *APIError the director's answer produced.
	Err error
}

// Error implements the error interface.
func (e *ConfigNotFoundError) Error() string {
	method := e.Method
	if method != "" {
		method += " "
	}

	return fmt.Sprintf("the director answered %s/configs/<id> with 404 for ID %q. "+
		"Check that the ID is right, that it was not deleted or replaced by a "+
		"newer version, and that the UAA client has the scope to see it", method, e.ID)
}

// Unwrap returns the APIError behind the answer.
func (e *ConfigNotFoundError) Unwrap() error {
	return e.Err
}

// Is reports a match with ErrNotFound.
func (e *ConfigNotFoundError) Is(target error) bool {
	return target == ErrNotFound
}

// InstanceNotFoundError reports that the director answered GET
// /deployments/<deployment>/jobs/<job>/<index_or_id> with HTTP 404 and error
// code 70001, which means the deployment exists but holds no instance that
// matches the job and the index or UUID.
type InstanceNotFoundError struct {
	Deployment string
	Job        string
	IndexOrID  string
}

// Error implements the error interface.
func (e *InstanceNotFoundError) Error() string {
	return fmt.Sprintf("the director found no instance %s/%s in deployment %q. "+
		"Check the job name, and that the index or UUID belongs to this deployment",
		e.Job, e.IndexOrID, e.Deployment)
}

// Is reports a match with ErrNotFound.
func (e *InstanceNotFoundError) Is(target error) bool {
	return target == ErrNotFound
}

// DiffError reports a diff that the director could not compute. The director
// answers such a diff with HTTP 200, or with HTTP 400 on the generic config
// route, and a body that holds an empty diff and an error message, which for a
// deployment ends with the Ruby backtrace of the failure. Deployments().Diff
// and the Diff methods of Configs(), CloudConfigs(), RuntimeConfigs(), and
// CPIConfigs() return it, and callers test for it with errors.As.
type DiffError struct {
	// Target names what was diffed, such as "deployment cf" or "cloud config".
	Target string
	// Message is the whole text the director sent, backtrace included.
	Message string
	// Cause is the APIError the director's HTTP 400 answer became, and it is
	// nil when the director answered a failed diff with HTTP 200. Unwrap
	// returns it, so errors.As finds the APIError.
	Cause *APIError
}

// Unwrap returns the APIError behind the error, or nil when the director
// answered with HTTP 200, so errors.As(err, &apiErr) keeps working for a
// caller that tested a config diff for an APIError.
func (e *DiffError) Unwrap() error {
	if e.Cause == nil {
		return nil
	}

	return e.Cause
}

// Error implements the error interface. It gives the first line of the
// director's message and leaves the backtrace in Message.
func (e *DiffError) Error() string {
	first, _, _ := strings.Cut(strings.TrimSpace(e.Message), "\n")

	return fmt.Sprintf("diffing %s: the director could not compute the diff: %s", e.Target, strings.TrimSpace(first))
}

// ReleaseJobNotFoundError reports that the director answered GET /jobs with
// HTTP 404, which means it has no release with that name, or the release has
// no job with that name and fingerprint. The director sends an empty body for
// this answer, so the error replaces an APIError that would say nothing.
type ReleaseJobNotFoundError struct {
	Release     string
	Name        string
	Fingerprint string
}

// Error implements the error interface.
func (e *ReleaseJobNotFoundError) Error() string {
	return fmt.Sprintf("the director answered GET /jobs with 404 for job %q with fingerprint %q in release %q. "+
		"Check the release name, the job name, and that the fingerprint belongs to an uploaded release version",
		e.Name, e.Fingerprint, e.Release)
}

// Is reports a match with ErrNotFound.
func (e *ReleaseJobNotFoundError) Is(target error) bool {
	return target == ErrNotFound
}

// IsNotFound reports whether the director said a resource does not exist. It
// is true for a JSON error body that answers 404 with a code, such as
// TaskNotFound (10000), UserNotFound (20000), or DeploymentNotFound (70000),
// and for those three codes whatever the status. The director answers an
// unknown route with a plain 404, which IsRouteNotFound reports instead. It
// is also true for a ConfigNotFoundError, and for any error that matches
// ErrNotFound.
func IsNotFound(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return true
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case ErrorCodeTaskNotFound,
			ErrorCodeUserNotFound,
			ErrorCodeDeploymentNotFound:
			return true
		}

		return apiErr.StatusCode == http.StatusNotFound && apiErr.Code != 0
	}

	return false
}

// IsRouteNotFound reports whether the director answered 404 with a body that
// is not its JSON error shape, which is how it answers a route it does not
// serve, such as "Endpoint '/tasks/42' not found.". That is a routing error,
// not a missing resource, so IsNotFound is false for it. It is also false for
// any error that matches ErrNotFound, such as a ConfigNotFoundError, whose
// wrapped APIError is a plain 404 but which says the config is missing and
// not that the route is.
func IsRouteNotFound(err error) bool {
	if errors.Is(err, ErrNotFound) {
		return false
	}

	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusNotFound &&
		apiErr.Code == 0
}

// IsUnauthorized reports an authentication error, which is an HTTP 401 for a
// token or credentials the director did not accept. The director sends that
// answer as plain text with no code. A 401 with code 600000
// (ErrorCodeUnauthorizedToAccessDeployment) refuses a valid token that lacks
// a scope, so IsForbidden reports it and IsUnauthorized does not.
func IsUnauthorized(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		apiErr.StatusCode == http.StatusUnauthorized &&
		apiErr.Code != ErrorCodeUnauthorizedToAccessDeployment
}

// IsForbidden reports an authorization error, where the director knows the
// caller but refuses the request. That is an HTTP 403, such as code 810009
// for a link provider that is not shared, or a 401 with code 600000
// (ErrorCodeUnauthorizedToAccessDeployment), which the director sends when a
// valid token lacks the scope a route needs.
func IsForbidden(err error) bool {
	var apiErr *APIError

	return errors.As(err, &apiErr) &&
		(apiErr.StatusCode == http.StatusForbidden || apiErr.Code == ErrorCodeUnauthorizedToAccessDeployment)
}

// ParseAPIError parses a BOSH Director error response from JSON.
func ParseAPIError(data []byte) (*APIError, error) {
	var apiErr APIError

	err := json.Unmarshal(data, &apiErr)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal API error: %w", err)
	}

	return &apiErr, nil
}

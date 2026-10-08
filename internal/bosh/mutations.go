package bosh

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

// maxContextIDLength is the longest context ID the director accepts in its
// X-Bosh-Context-Id header.
const maxContextIDLength = 64

// lostAnswerLookupTimeout bounds the lookup by context ID that a deploy or
// a delete makes once its answer was lost.
const lostAnswerLookupTimeout = 30 * time.Second

// allJobs is the job a job-state change targets to act on every instance
// of a deployment. boshapi sends it as a literal "*", as bosh-cli does.
const allJobs = "*"

// The states a job-state change asks the director for.
const (
	jobStateRestart  = "restart"
	jobStateStopped  = "stopped"
	jobStateStarted  = "started"
	jobStateRecreate = "recreate"
)

// mutationContext returns the context a mutation sends its request on and
// the context ID the request carries. A caller that brought no ID gets a
// fresh UUID. An ID the caller brought is checked against what the director
// accepts, which is at most 64 characters of printable ASCII, because
// boshapi would refuse it after the adapter had already named it in an
// error. A refused ID returns an error that wraps ErrInvalidContextID, and
// nothing is sent.
func mutationContext(ctx context.Context, operation string) (context.Context, string, error) {
	contextID := boshapi.ContextIDFrom(ctx)
	if contextID == "" {
		contextID = uuid.NewString()

		return boshapi.WithContextID(ctx, contextID), contextID, nil
	}

	if len(contextID) > maxContextIDLength {
		return ctx, contextID, fmt.Errorf("%w: %s was given the context ID %q, which is %d characters long, "+
			"but the director accepts at most %d, so nothing was sent",
			ErrInvalidContextID, operation, contextID, len(contextID), maxContextIDLength)
	}

	for offset := range len(contextID) {
		if contextID[offset] < 0x20 || contextID[offset] > 0x7e {
			return ctx, contextID, fmt.Errorf("%w: %s was given the context ID %q, which is %d characters long "+
				"and has a byte outside printable ASCII at offset %d, so nothing was sent",
				ErrInvalidContextID, operation, contextID, len(contextID), offset)
		}
	}

	return ctx, contextID, nil
}

// mutation describes one request that changes something on the director,
// for the errors and log lines about it.
type mutation struct {
	// operation is the Director method that sends the request.
	operation string
	// deployment is the deployment the request acts on, or empty for an
	// upload or a cleanup.
	deployment string
	// contextID is the context ID the request carries.
	contextID string
	// what says what the request does, for a failure message.
	what string
	// lookup is true for a request whose task the director tags with the
	// context ID, which are deploys and deletes, so a lost answer is
	// looked up by that ID.
	lookup bool
}

// accepted turns a mutation's answer into the task the director's redirect
// names, as soon as the director has accepted the request. An error that
// may hide a request the director accepted becomes an OutcomeUnknownError,
// even when the caller's context has ended, because the caller must not
// resend such a request. A deploy or a delete first looks for its task by
// the context ID, and returns that task when the director lists it. Other
// mutations make no lookup, and their error has NoLookup set. Any other
// error keeps the director's answer or the transport failure reachable
// through errors.As, so a follower can see that no task started.
func (d *DirectorAdapter) accepted(ctx context.Context, call mutation, task *boshapi.Task, err error) (*Task, error) {
	if err == nil && task == nil {
		err = ErrEmptyDirectorAnswer
	}

	if err != nil {
		d.log.Errorf("Failed to %s (context ID %s): %v", call.what, call.contextID, err)

		if boshapi.IsOutcomeUnknown(err) || errors.Is(err, ErrEmptyDirectorAnswer) {
			return d.lostAnswer(ctx, call, err)
		}

		return nil, d.requestFailure(call.what, err)
	}

	result := convertAPITask(*task)

	d.log.Infof("The director accepted the request to %s as task %d (context ID %s)", call.what, result.ID, call.contextID)

	return &result, nil
}

// lostAnswer settles a mutation whose answer was lost. A deploy or a
// delete looks for its task by the context ID it carried, on a context of
// its own bounded by lostAnswerLookupTimeout, so the lookup runs even when
// the caller's context has ended. The director's task for that ID is the
// request's task, and it is returned with no error. When the director
// lists none, or the lookup fails, the outcome stays unknown, and a failed
// lookup is only logged. Every other mutation makes no lookup, because the
// director may not tag its task with the ID.
func (d *DirectorAdapter) lostAnswer(ctx context.Context, call mutation, err error) (*Task, error) {
	unknown := &OutcomeUnknownError{
		Operation:  call.operation,
		Director:   d.address,
		Deployment: call.deployment,
		ContextID:  call.contextID,
		NoLookup:   !call.lookup,
		Err:        err,
	}

	if !call.lookup {
		return nil, unknown
	}

	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lostAnswerLookupTimeout)
	defer cancel()

	found, lookupErr := d.findTaskByContextID(lookupCtx, call.contextID)

	switch {
	case lookupErr != nil:
		d.log.Errorf("The answer to the request to %s was lost, and the lookup of its task by the context ID %s failed, so its outcome stays unknown: %v",
			call.what, call.contextID, lookupErr)
	case found == nil || found.ID <= 0:
		d.log.Infof("The answer to the request to %s was lost, and the director lists no task with its context ID %s yet, so its outcome stays unknown",
			call.what, call.contextID)
	default:
		d.log.Infof("The answer to the request to %s was lost, and the director lists task %d (state %s) with its context ID %s, so that task is the request's",
			call.what, found.ID, found.State, call.contextID)

		return found, nil
	}

	return nil, unknown
}

// CreateDeployment sends the manifest to the director and returns the
// deploy task as soon as the director accepts it. The director creates the
// deployment the manifest names, or updates it when it exists.
func (d *DirectorAdapter) CreateDeployment(ctx context.Context, manifest string) (_ *Task, err error) {
	defer d.explainContext(ctx, "CreateDeployment", &err)

	deploymentName := ExtractDeploymentName(manifest)
	if deploymentName == "" {
		d.log.Errorf("Could not extract deployment name from manifest")

		return nil, ErrDeploymentNameExtractionFailed
	}

	return d.deploy(ctx, "CreateDeployment", deploymentName, manifest)
}

// UpdateDeployment sends a new manifest for the deployment and returns the
// deploy task as soon as the director accepts it.
func (d *DirectorAdapter) UpdateDeployment(ctx context.Context, name, manifest string) (_ *Task, err error) {
	defer d.explainContext(ctx, "UpdateDeployment", &err)

	return d.deploy(ctx, "UpdateDeployment", name, manifest)
}

func (d *DirectorAdapter) deploy(ctx context.Context, operation, deploymentName, manifest string) (*Task, error) {
	ctx, contextID, err := mutationContext(ctx, operation)
	if err != nil {
		return nil, err
	}

	d.log.Infof("Deploying deployment %s (context ID %s)", deploymentName, contextID)
	d.log.Debugf("Manifest size: %d bytes", len(manifest))

	task, err := d.api.Deployments().Deploy(ctx, manifest, nil)

	return d.accepted(ctx, mutation{
		operation:  operation,
		deployment: deploymentName,
		contextID:  contextID,
		what:       "deploy deployment " + deploymentName,
		lookup:     true,
	}, task, err)
}

// DeleteDeployment deletes a deployment without force and returns the
// delete task as soon as the director accepts it. The director's answer
// that it has no such deployment, code 70000, wraps ErrDeploymentNotFound
// and keeps the director's answer reachable. A 404 for a route the director
// does not serve stays a plain *boshapi.APIError.
func (d *DirectorAdapter) DeleteDeployment(ctx context.Context, name string) (_ *Task, err error) {
	defer d.explainContext(ctx, "DeleteDeployment", &err)

	ctx, contextID, err := mutationContext(ctx, "DeleteDeployment")
	if err != nil {
		return nil, err
	}

	d.log.Infof("Deleting deployment %s (context ID %s)", name, contextID)

	task, err := d.api.Deployments().Delete(ctx, name, nil)

	var apiErr *boshapi.APIError
	if errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeDeploymentNotFound {
		d.log.Infof("The director has no deployment %s to delete", name)

		return nil, fmt.Errorf("%w: %w", ErrDeploymentNotFound, err)
	}

	return d.accepted(ctx, mutation{
		operation:  "DeleteDeployment",
		deployment: name,
		contextID:  contextID,
		what:       "delete deployment " + name,
		lookup:     true,
	}, task, err)
}

// UploadRelease asks the director to fetch a release from the URL and
// returns the upload task as soon as the director accepts it.
func (d *DirectorAdapter) UploadRelease(ctx context.Context, url, sha1 string) (_ *Task, err error) {
	defer d.explainContext(ctx, "UploadRelease", &err)

	ctx, contextID, err := mutationContext(ctx, "UploadRelease")
	if err != nil {
		return nil, err
	}

	d.log.Infof("Uploading release from URL %s (context ID %s)", url, contextID)
	d.log.Debugf("Release SHA1: %s", sha1)

	task, err := d.api.Releases().Upload(ctx, &boshapi.UploadReleaseOptions{URL: url, SHA1: sha1})

	return d.accepted(ctx, mutation{
		operation: "UploadRelease",
		contextID: contextID,
		what:      "upload release from " + url,
	}, task, err)
}

// UploadStemcell asks the director to fetch a stemcell from the URL and
// returns the upload task as soon as the director accepts it.
func (d *DirectorAdapter) UploadStemcell(ctx context.Context, url, sha1 string) (_ *Task, err error) {
	defer d.explainContext(ctx, "UploadStemcell", &err)

	ctx, contextID, err := mutationContext(ctx, "UploadStemcell")
	if err != nil {
		return nil, err
	}

	d.log.Infof("Uploading stemcell from URL %s (context ID %s)", url, contextID)
	d.log.Debugf("Stemcell SHA1: %s", sha1)

	task, err := d.api.Stemcells().Upload(ctx, &boshapi.UploadStemcellOptions{URL: url, SHA1: sha1})

	return d.accepted(ctx, mutation{
		operation: "UploadStemcell",
		contextID: contextID,
		what:      "upload stemcell from " + url,
	}, task, err)
}

// Cleanup runs the director's cleanup and returns its task as soon as the
// director accepts it.
func (d *DirectorAdapter) Cleanup(ctx context.Context, removeAll bool) (_ *Task, err error) {
	defer d.explainContext(ctx, "Cleanup", &err)

	ctx, contextID, err := mutationContext(ctx, "Cleanup")
	if err != nil {
		return nil, err
	}

	d.log.Infof("Running BOSH cleanup (removeAll: %v, context ID %s)", removeAll, contextID)

	task, err := d.api.Cleanup().Cleanup(ctx, &boshapi.CleanupRequest{Config: boshapi.CleanupConfig{RemoveAll: removeAll}})

	return d.accepted(ctx, mutation{
		operation: "Cleanup",
		contextID: contextID,
		what:      "run cleanup",
	}, task, err)
}

// RestartDeployment restarts every instance of the deployment.
func (d *DirectorAdapter) RestartDeployment(ctx context.Context, name string, opts RestartOpts) (_ *Task, err error) {
	defer d.explainContext(ctx, "RestartDeployment", &err)

	return d.changeJobState(ctx, "RestartDeployment", name, &boshapi.UpdateStateOptions{
		NewState:    jobStateRestart,
		SkipDrain:   opts.SkipDrain,
		Canaries:    opts.Canaries,
		MaxInFlight: opts.MaxInFlight,
	})
}

// StopDeployment stops every instance of the deployment. A hard stop
// deletes the VMs and keeps their persistent disks.
func (d *DirectorAdapter) StopDeployment(ctx context.Context, name string, opts StopOpts) (_ *Task, err error) {
	defer d.explainContext(ctx, "StopDeployment", &err)

	return d.changeJobState(ctx, "StopDeployment", name, &boshapi.UpdateStateOptions{
		NewState:    jobStateStopped,
		SkipDrain:   opts.SkipDrain,
		Hard:        opts.Hard,
		Canaries:    opts.Canaries,
		MaxInFlight: opts.MaxInFlight,
	})
}

// StartDeployment starts every instance of the deployment.
func (d *DirectorAdapter) StartDeployment(ctx context.Context, name string, opts StartOpts) (_ *Task, err error) {
	defer d.explainContext(ctx, "StartDeployment", &err)

	return d.changeJobState(ctx, "StartDeployment", name, &boshapi.UpdateStateOptions{
		NewState:    jobStateStarted,
		Canaries:    opts.Canaries,
		MaxInFlight: opts.MaxInFlight,
	})
}

// RecreateDeployment recreates every instance of the deployment.
func (d *DirectorAdapter) RecreateDeployment(ctx context.Context, name string, opts RecreateOpts) (_ *Task, err error) {
	defer d.explainContext(ctx, "RecreateDeployment", &err)

	return d.changeJobState(ctx, "RecreateDeployment", name, &boshapi.UpdateStateOptions{
		NewState:    jobStateRecreate,
		SkipDrain:   opts.SkipDrain,
		Fix:         opts.Fix,
		DryRun:      opts.DryRun,
		Canaries:    opts.Canaries,
		MaxInFlight: opts.MaxInFlight,
	})
}

// changeJobState asks the director to change the state of every job of the
// deployment, and returns the task as soon as the director accepts it.
func (d *DirectorAdapter) changeJobState(ctx context.Context, operation, name string, opts *boshapi.UpdateStateOptions) (*Task, error) {
	ctx, contextID, err := mutationContext(ctx, operation)
	if err != nil {
		return nil, err
	}

	d.log.Infof("Changing every job of deployment %s to %s (context ID %s)", name, opts.NewState, contextID)

	task, err := d.api.Instances().UpdateJobState(ctx, name, allJobs, opts)

	return d.accepted(ctx, mutation{
		operation:  operation,
		deployment: name,
		contextID:  contextID,
		what:       "change every job of deployment " + name + " to " + opts.NewState,
	}, task, err)
}

package bosh

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// UnknownOutcomeGrace is how long an operation whose director answer was
// lost stays unknown before a lookup that finds no task for its context ID
// may fail it. It is far longer than a request's timeout, so a task the
// director accepted while its answer was being lost has long since been
// listed by the time it ends.
const UnknownOutcomeGrace = 15 * time.Minute

// ErrOutcomeUnknown is the sentinel an OutcomeUnknownError wraps. It means a
// mutation was sent to the director and no answer came back, so the
// director may or may not have acted on it.
var ErrOutcomeUnknown = errors.New("the director's answer was lost, so the outcome of the request is unknown")

// ErrBrokerShuttingDown is the cause the broker cancels its service context
// with when it shuts down. A wait for a read-only director task that ends
// with this cause sends the director no cancel, because such a task is
// harmless and ends on its own.
var ErrBrokerShuttingDown = errors.New("the broker is shutting down")

// ErrInvalidContextID is the sentinel a mutation returns when its caller's
// context ID is one the director would refuse. Nothing is sent.
var ErrInvalidContextID = errors.New("the context ID is not one the BOSH director accepts")

// ErrSSHNotPermitted is the sentinel an SSH setup returns when the director
// refuses the broker's client for the deployment.
var ErrSSHNotPermitted = errors.New("the broker's BOSH director client lacks the permission to set up SSH")

// ErrSSHTargetInvalid is the sentinel an SSH call returns when its target
// is not one instance. Nothing is sent.
var ErrSSHTargetInvalid = errors.New("an SSH target must name one instance by its group and a non-negative index")

// OutcomeUnknownError is the error a mutation returns when the director may
// have accepted it and its answer never came back. Blacksmith never resends
// such a request on its own, because a second copy could act twice.
type OutcomeUnknownError struct {
	// Operation is the Director method that was running.
	Operation string
	// Director is the director address the request went to.
	Director string
	// Deployment is the deployment the request was for, when it had one.
	Deployment string
	// ContextID is the context ID the request carried, when it carried one.
	ContextID string
	// Config names the config a config write was for, as "<type>/<name>".
	// It is empty for every other request.
	Config string
	// NoLookup is true when Blacksmith made no lookup for the request's
	// task, so the message never says it looked for one.
	NoLookup bool
	// LookupFailed is true when Blacksmith tried to look for the request's
	// task by its context ID and the lookup itself failed, so the message
	// never says it found none.
	LookupFailed bool
	// SSHUser is the throwaway user an SSH setup created or may have
	// created, and SSHInstance the instance it is on. Both are empty for
	// every other request.
	SSHUser     string
	SSHInstance string
	// Err is the error the request saw.
	Err error
}

// Error says what Blacksmith did not do and where the operator can check
// what the director did, and names the SSH user a lost setup may have
// created.
func (e *OutcomeUnknownError) Error() string {
	return withSSHUser(e.message(), e.SSHUser, e.SSHInstance)
}

// Unwrap returns both the sentinel and the error the request saw, so
// errors.Is(err, ErrOutcomeUnknown) holds and the request's own error stays
// reachable.
func (e *OutcomeUnknownError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrOutcomeUnknown}
	}

	return []error{ErrOutcomeUnknown, e.Err}
}

// message is the error's text before any SSH user is named.
func (e *OutcomeUnknownError) message() string {
	var msg strings.Builder

	fmt.Fprintf(&msg, "%s on BOSH director %s got no answer", e.Operation, e.Director)

	if e.Deployment != "" {
		fmt.Fprintf(&msg, " for deployment %s", e.Deployment)
	}

	if e.ContextID != "" {
		fmt.Fprintf(&msg, " (context ID %s)", e.ContextID)
	}

	// A config write creates no director task, and the transport text
	// tells the operator to look for one, so it stays out of that message.
	// It is still reachable through Unwrap.
	//
	// A request that made no lookup leaves it out too, because the text
	// tells the operator to look a task up by the context ID, which the
	// director may not store for that request.
	if e.Err != nil && e.Config == "" && !e.NoLookup {
		fmt.Fprintf(&msg, " (%v)", e.Err)
	}

	msg.WriteString(". The director may have accepted the request, and Blacksmith did not resend it. ")

	if e.Config != "" {
		configType, configName, _ := strings.Cut(e.Config, "/")

		fmt.Fprintf(&msg, "A config write creates no director task, so there is no task to look for. "+
			"Check whether the write landed by reading GET %s/configs?type=%s&name=%s&latest=true. "+
			"For a create, the config is present with the new content. "+
			"For a delete, the latest list for that type and name is empty",
			e.Director, configType, configName)

		return msg.String()
	}

	if e.NoLookup {
		if e.Deployment != "" {
			fmt.Fprintf(&msg, "Check /b/tasks for a task on the deployment %s.", e.Deployment)
		} else {
			fmt.Fprintf(&msg, "Check /b/tasks for a recent %s task.", e.Operation)
		}

		return msg.String()
	}

	// A lookup that failed found nothing either way, so the message says
	// the task could not be sought rather than that none was found.
	switch {
	case e.ContextID != "" && e.LookupFailed:
		msg.WriteString("Blacksmith could not look for a task with that context ID, because the lookup failed. ")
	case e.ContextID != "":
		msg.WriteString("Blacksmith looked for a task with that context ID and found none yet. ")
	}

	switch {
	case e.Deployment != "":
		fmt.Fprintf(&msg, "Check /b/tasks for a task on the deployment %s", e.Deployment)
	case e.ContextID != "":
		fmt.Fprintf(&msg, "Check /b/tasks for the task with context ID %s", e.ContextID)
	default:
		msg.WriteString("Check /b/tasks for a recent task")
	}

	return msg.String()
}

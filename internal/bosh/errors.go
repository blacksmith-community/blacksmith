package bosh

import (
	"errors"
	"fmt"
	"strings"
)

// ErrOutcomeUnknown is the sentinel an OutcomeUnknownError wraps. It means a
// mutation was sent to the director and no answer came back, so the
// director may or may not have acted on it.
var ErrOutcomeUnknown = errors.New("the director's answer was lost, so the outcome of the request is unknown")

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
	// Err is the error the request saw.
	Err error
}

// Error says what Blacksmith did not do and where the operator can check
// what the director did.
func (e *OutcomeUnknownError) Error() string {
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
	if e.Err != nil && e.Config == "" {
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

	if e.ContextID != "" {
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

// Unwrap returns both the sentinel and the error the request saw, so
// errors.Is(err, ErrOutcomeUnknown) holds and the request's own error stays
// reachable.
func (e *OutcomeUnknownError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrOutcomeUnknown}
	}

	return []error{ErrOutcomeUnknown, e.Err}
}

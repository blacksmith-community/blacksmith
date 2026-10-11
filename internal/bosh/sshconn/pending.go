package sshconn

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// ErrUnusableCleanup means a pending cleanup cannot be sent as it stands,
// because its user is not one Blacksmith makes or its target does not name
// one instance.
var ErrUnusableCleanup = errors.New("the pending SSH cleanup cannot be used")

// userForm is the form NewCredentials gives every user name.
var userForm = regexp.MustCompile(`^` + userPrefix + `[0-9a-f]{15}$`)

// PendingCleanup is a throwaway SSH user that may still be on a VM. The
// broker records one before it sends a setup and clears it once a cleanup
// task for the user ends done, so a cleanup that a restart, a lost answer,
// or a failed task missed is sent again later.
type PendingCleanup struct {
	// User is the throwaway user the setup creates.
	User string `json:"user"`
	// Deployment is the deployment the setup targets.
	Deployment string `json:"deployment"`
	// Target is the instance selection the setup and every cleanup use.
	Target boshapi.SSHTarget `json:"target"`
	// SetupContextID is the context ID the setup was sent with.
	SetupContextID string `json:"setup_context_id"`
	// CreatedAt is when the record was written, before the setup was sent.
	CreatedAt time.Time `json:"created_at"`
	// Attempts counts the cleanups sent for the record that did not end
	// done.
	Attempts int `json:"attempts"`
	// LastError is the error of the last cleanup that did not end done.
	LastError string `json:"last_error,omitempty"`
	// LastAttemptAt is when that cleanup was sent.
	LastAttemptAt time.Time `json:"last_attempt_at,omitzero"`
}

// CheckUser refuses a user name that NewCredentials would never make. The
// name keys the record, so a name with a slash or an empty one would point
// somewhere else.
func CheckUser(user string) error {
	if !userForm.MatchString(user) {
		return fmt.Errorf("%w: user %q is not bosh_ followed by 15 lowercase hex characters", ErrUnusableCleanup, user)
	}

	return nil
}

// Validate refuses a record whose cleanup would be unsafe or could never
// work. Its user must have the form NewCredentials gives, it must name a
// deployment, and its target must name an instance group and at least one
// instance, because an empty target selects every instance in the
// deployment.
func (p PendingCleanup) Validate() error {
	err := CheckUser(p.User)
	if err != nil {
		return err
	}

	switch {
	case p.Deployment == "":
		return fmt.Errorf("%w: the record for user %s names no deployment", ErrUnusableCleanup, p.User)
	case p.Target.Job == "" || len(p.Target.Indices)+len(p.Target.IDs) == 0:
		return fmt.Errorf("%w: the record for user %s targets %s of deployment %s, not one instance",
			ErrUnusableCleanup, p.User, TargetName(p.Target), p.Deployment)
	default:
		return nil
	}
}

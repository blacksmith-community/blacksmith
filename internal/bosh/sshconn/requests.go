package sshconn

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// The commands the director's SSH route runs. boshapi posts the request as
// the caller builds it, so the caller sets the command.
const (
	commandSetup   = "setup"
	commandCleanup = "cleanup"
)

// SetupRequest builds the body that asks the director to create user on
// the target's instances with publicKey, in authorized-keys form, as its
// login key.
func SetupRequest(deployment string, target boshapi.SSHTarget, user, publicKey string) *boshapi.SSHRequest {
	return &boshapi.SSHRequest{
		Command:        commandSetup,
		DeploymentName: deployment,
		Target:         target,
		Params:         boshapi.SSHParams{User: user, PublicKey: publicKey},
	}
}

// CleanupRequest builds the body that asks the director to remove user
// from the target's instances. The target must be the one the setup used,
// because an empty target selects every instance in the deployment.
func CleanupRequest(deployment string, target boshapi.SSHTarget, user string) *boshapi.SSHRequest {
	return &boshapi.SSHRequest{
		Command:        commandCleanup,
		DeploymentName: deployment,
		Target:         target,
		Params:         boshapi.SSHParams{UserRegex: UserRegex(user)},
	}
}

// UserRegex returns the pattern that matches user and no other name, not
// even a longer one that starts the same way.
func UserRegex(user string) string {
	return "^" + regexp.QuoteMeta(user) + "$"
}

// TargetName names the target the way an operator names an instance, as
// group/index or group/ID.
func TargetName(target boshapi.SSHTarget) string {
	members := make([]string, 0, len(target.Indices)+len(target.IDs))

	for _, index := range target.Indices {
		members = append(members, strconv.Itoa(index))
	}

	members = append(members, target.IDs...)

	switch {
	case target.Job == "" && len(members) == 0:
		return "every instance"
	case len(members) == 0:
		return target.Job
	default:
		return target.Job + "/" + strings.Join(members, ",")
	}
}

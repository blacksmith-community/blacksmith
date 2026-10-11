package sshconn_test

import (
	"errors"
	"testing"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// pendingTestUser has the form NewCredentials gives.
const pendingTestUser = "bosh_0123456789abcde"

// A record can only be swept when its user has the agent's form and its
// target names one instance, because a cleanup with an empty target selects
// every instance in the deployment.
func TestPendingCleanupRefusesARecordItCannotUse(t *testing.T) {
	t.Parallel()

	usable := sshconn.PendingCleanup{
		User:       pendingTestUser,
		Deployment: "redis-1",
		Target:     boshapi.SSHTarget{Job: "redis", Indices: []int{0}},
	}

	err := usable.Validate()
	if err != nil {
		t.Fatalf("Validate refused a usable record: %v", err)
	}

	byID := usable
	byID.Target = boshapi.SSHTarget{Job: "redis", IDs: []string{"abc"}}

	err = byID.Validate()
	if err != nil {
		t.Fatalf("Validate refused a record that targets an instance by its ID: %v", err)
	}

	tests := []struct {
		name   string
		change func(*sshconn.PendingCleanup)
	}{
		{"no user", func(p *sshconn.PendingCleanup) { p.User = "" }},
		{"a user with a slash", func(p *sshconn.PendingCleanup) { p.User = "bosh_0123456789a/cd" }},
		{"a user without the prefix", func(p *sshconn.PendingCleanup) { p.User = "ubuntu" }},
		{"no deployment", func(p *sshconn.PendingCleanup) { p.Deployment = "" }},
		{"no instance group", func(p *sshconn.PendingCleanup) { p.Target.Job = "" }},
		{"no instance", func(p *sshconn.PendingCleanup) { p.Target.Indices = nil }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			record := usable
			record.Target.Indices = append([]int(nil), usable.Target.Indices...)
			test.change(&record)

			err := record.Validate()
			if !errors.Is(err, sshconn.ErrUnusableCleanup) {
				t.Fatalf("Validate = %v, want ErrUnusableCleanup", err)
			}
		})
	}
}

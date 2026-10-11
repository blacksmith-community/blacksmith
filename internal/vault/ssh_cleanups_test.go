package vault_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/vault"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Values the SSH cleanup record tests share. sshCleanupsFolder is where
// the broker keeps its pending SSH cleanups, relative to the mount.
const (
	sshCleanupsFolder = "secret/blacksmith/ssh-cleanups/"
	cleanupDeployment = "redis-1"
	cleanupUserKey    = "user"
)

// pendingCleanup returns a usable record for user.
func pendingCleanup(user string, created time.Time) sshconn.PendingCleanup {
	return sshconn.PendingCleanup{
		User:           user,
		Deployment:     cleanupDeployment,
		Target:         boshapi.SSHTarget{Job: "redis", Indices: []int{2}},
		SetupContextID: "ctx-setup-" + user,
		CreatedAt:      created,
	}
}

// sameCleanup reports whether two records hold the same values. The target
// is compared by name, because a target with no IDs reads back with an
// empty list in place of nil.
func sameCleanup(got, want sshconn.PendingCleanup) bool {
	return got.User == want.User &&
		got.Deployment == want.Deployment &&
		sshconn.TargetName(got.Target) == sshconn.TargetName(want.Target) &&
		got.SetupContextID == want.SetupContextID &&
		got.CreatedAt.Equal(want.CreatedAt) &&
		got.Attempts == want.Attempts &&
		got.LastError == want.LastError &&
		got.LastAttemptAt.Equal(want.LastAttemptAt)
}

// folderKeys lists the keys the records folder holds in Vault's metadata,
// which still names a record that was deleted but not destroyed.
func folderKeys(t *testing.T, client *vault.Vault) []interface{} {
	t.Helper()

	apiClient, err := client.GetAPIClient()
	if err != nil {
		t.Fatalf("GetAPIClient: %v", err)
	}

	folder, err := apiClient.Logical().ListWithContext(t.Context(), "secret/metadata/"+sshCleanupsFolder)
	if err != nil {
		t.Fatalf("listing the folder: %v", err)
	}

	if folder == nil {
		return nil
	}

	keys, _ := folder.Data["keys"].([]interface{})

	return keys
}

// A record reads back as it was written, an update replaces it, and a
// clear removes it so completely that the folder no longer lists its user.
func TestVaultSSHCleanupRecordsRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	created := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	first := pendingCleanup("bosh_0123456789abcde", created)
	second := pendingCleanup("bosh_fedcba987654321", created.Add(time.Minute))

	for _, record := range []sshconn.PendingCleanup{second, first} {
		err := client.RecordSSHCleanup(ctx, record)
		if err != nil {
			t.Fatalf("RecordSSHCleanup(%s): %v", record.User, err)
		}
	}

	listed, err := client.ListSSHCleanups(ctx)
	if err != nil {
		t.Fatalf("ListSSHCleanups: %v", err)
	}

	if len(listed) != 2 || !sameCleanup(listed[0], first) || !sameCleanup(listed[1], second) {
		t.Fatalf("listed %+v, want the two records oldest first", listed)
	}

	first.Attempts = 1
	first.LastError = "the agent on redis/2 did not answer"
	first.LastAttemptAt = created.Add(10 * time.Minute)

	err = client.RecordSSHCleanup(ctx, first)
	if err != nil {
		t.Fatalf("RecordSSHCleanup over the first record: %v", err)
	}

	err = client.ClearSSHCleanup(ctx, second.User)
	if err != nil {
		t.Fatalf("ClearSSHCleanup(%s): %v", second.User, err)
	}

	listed, err = client.ListSSHCleanups(ctx)
	if err != nil {
		t.Fatalf("ListSSHCleanups after the clear: %v", err)
	}

	if len(listed) != 1 || !sameCleanup(listed[0], first) {
		t.Fatalf("listed %+v after the clear, want only the updated first record", listed)
	}

	if keys := folderKeys(t, client); slices.Contains(keys, interface{}(second.User)) {
		t.Errorf("the folder still lists the cleared user %s: %v", second.User, keys)
	}

	err = client.ClearSSHCleanup(ctx, second.User)
	if err != nil {
		t.Errorf("clearing a record that is already gone: %v", err)
	}
}

// The store refuses a record it could never sweep, and a user name that
// would point outside the folder.
func TestVaultSSHCleanupRefusesAnUnusableRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	unusable := pendingCleanup("bosh_0123456789abcde", time.Now())
	unusable.Target = boshapi.SSHTarget{}

	err := client.RecordSSHCleanup(ctx, unusable)
	if !errors.Is(err, sshconn.ErrUnusableCleanup) {
		t.Fatalf("RecordSSHCleanup with an empty target = %v, want ErrUnusableCleanup", err)
	}

	err = client.ClearSSHCleanup(ctx, "../registrations/r1")
	if !errors.Is(err, sshconn.ErrUnusableCleanup) {
		t.Fatalf("ClearSSHCleanup with a path for a user = %v, want ErrUnusableCleanup", err)
	}
}

// A record the store cannot read, or one that names another user or no
// instance, is left out of the listing and named in its error, and the
// records it can read still come back.
func TestVaultSSHCleanupListNamesAnUnreadableRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	good := pendingCleanup("bosh_0123456789abcde", time.Now().UTC())

	err := client.RecordSSHCleanup(ctx, good)
	if err != nil {
		t.Fatalf("RecordSSHCleanup: %v", err)
	}

	writes := map[string]map[string]interface{}{
		"bosh_1111111111aaaaa": {cleanupUserKey: "bosh_1111111111aaaaa", "attempts": "many"},
		"bosh_2222222222bbbbb": {cleanupUserKey: "bosh_3333333333ccccc", "deployment": cleanupDeployment, "target": map[string]interface{}{"job": "redis", "indexes": []int{0}}},
		"bosh_4444444444ddddd": {cleanupUserKey: "bosh_4444444444ddddd", "deployment": cleanupDeployment, "target": map[string]interface{}{}},
	}

	for user, data := range writes {
		err := client.Put(ctx, sshCleanupsFolder+user, data)
		if err != nil {
			t.Fatalf("writing the record of %s: %v", user, err)
		}
	}

	listed, err := client.ListSSHCleanups(ctx)
	if !errors.Is(err, vault.ErrSSHCleanupUnreadable) {
		t.Fatalf("ListSSHCleanups = %v, want ErrSSHCleanupUnreadable", err)
	}

	for user := range writes {
		if !strings.Contains(err.Error(), user) {
			t.Errorf("the error does not name the unreadable record %s: %v", user, err)
		}
	}

	if len(listed) != 1 || !sameCleanup(listed[0], good) {
		t.Fatalf("listed %+v, want only the readable record", listed)
	}
}

// The records live under the secret/ folder, which holds the broker's own
// state, so the re-adoption at boot never takes one for an instance.
func TestSSHCleanupRecordsAreNotOperationRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newDevVault(t)

	err := client.RecordSSHCleanup(ctx, pendingCleanup("bosh_0123456789abcde", time.Now()))
	if err != nil {
		t.Fatalf("RecordSSHCleanup: %v", err)
	}

	err = client.Put(ctx, "instance-a/task", map[string]interface{}{"instance_id": "instance-a"})
	if err != nil {
		t.Fatalf("writing an instance's task record: %v", err)
	}

	ids, _, err := client.ListOperationRecordIDs(ctx)
	if err != nil {
		t.Fatalf("ListOperationRecordIDs: %v", err)
	}

	if !slices.Equal(ids, []string{"instance-a"}) {
		t.Fatalf("ListOperationRecordIDs = %v, want only instance-a", ids)
	}
}

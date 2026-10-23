package upgrade_test

import (
	"errors"
	"strings"
	"testing"

	"blacksmith/internal/upgrade"
)

// allPhases lists every VM phase, open and closed.
func allPhases() []upgrade.VMPhase {
	return append(openPhases(),
		upgrade.VMPhaseDone, upgrade.VMPhaseRolledBack, upgrade.VMPhaseUnrecovered, upgrade.VMPhaseReverted)
}

// openPhases lists every phase a resume may still act on.
func openPhases() []upgrade.VMPhase {
	return []upgrade.VMPhase{
		upgrade.VMPhasePlanned, upgrade.VMPhaseStaged, upgrade.VMPhaseBackedUp, upgrade.VMPhaseSwapping,
		upgrade.VMPhaseSwapped, upgrade.VMPhaseRestarted, upgrade.VMPhaseVerified, upgrade.VMPhaseRollingBack,
	}
}

type move struct{ from, to upgrade.VMPhase }

// allowedMoves holds the edges the state diagram draws, plus every open
// phase to unrecovered and to reverted.
func allowedMoves() map[move]bool {
	allowed := map[move]bool{
		{upgrade.VMPhasePlanned, upgrade.VMPhaseStaged}:          true,
		{upgrade.VMPhaseStaged, upgrade.VMPhaseBackedUp}:         true,
		{upgrade.VMPhaseBackedUp, upgrade.VMPhaseSwapping}:       true,
		{upgrade.VMPhaseSwapping, upgrade.VMPhaseSwapped}:        true,
		{upgrade.VMPhaseSwapped, upgrade.VMPhaseRestarted}:       true,
		{upgrade.VMPhaseRestarted, upgrade.VMPhaseVerified}:      true,
		{upgrade.VMPhaseVerified, upgrade.VMPhaseDone}:           true,
		{upgrade.VMPhaseSwapped, upgrade.VMPhaseRollingBack}:     true,
		{upgrade.VMPhaseRestarted, upgrade.VMPhaseRollingBack}:   true,
		{upgrade.VMPhaseVerified, upgrade.VMPhaseRollingBack}:    true,
		{upgrade.VMPhaseSwapping, upgrade.VMPhaseRollingBack}:    true,
		{upgrade.VMPhaseRollingBack, upgrade.VMPhaseRolledBack}:  true,
		{upgrade.VMPhaseRollingBack, upgrade.VMPhaseUnrecovered}: true,
		{upgrade.VMPhasePlanned, upgrade.VMPhaseUnrecovered}:     true,
	}

	for _, from := range openPhases() {
		allowed[move{from, upgrade.VMPhaseUnrecovered}] = true
		allowed[move{from, upgrade.VMPhaseReverted}] = true
	}

	return allowed
}

func TestAdvanceTransitions(t *testing.T) {
	t.Parallel()

	allowed := allowedMoves()

	// The named moves the resume table relies on, and the named refusals.
	named := map[move]bool{
		{upgrade.VMPhaseSwapping, upgrade.VMPhaseUnrecovered}:  true,
		{upgrade.VMPhaseRestarted, upgrade.VMPhaseUnrecovered}: true,
		{upgrade.VMPhasePlanned, upgrade.VMPhaseSwapped}:       false,
		{upgrade.VMPhaseRolledBack, upgrade.VMPhaseSwapping}:   false,
		{upgrade.VMPhaseUnrecovered, upgrade.VMPhaseStaged}:    false,
		{upgrade.VMPhaseDone, upgrade.VMPhaseReverted}:         false,
		{upgrade.VMPhaseRolledBack, upgrade.VMPhaseReverted}:   false,
		{upgrade.VMPhaseDone, upgrade.VMPhaseUnrecovered}:      false,
	}
	for step, legal := range named {
		if allowed[step] != legal {
			t.Fatalf("the expected table disagrees on %s to %s", step.from, step.to)
		}
	}

	// Every pair of known phases is checked against the table, so
	// nothing outside it is legal, a move to the same phase included.
	for _, from := range allPhases() {
		for _, target := range allPhases() {
			checkMove(t, move{from, target}, allowed[move{from, target}])
		}
	}
}

func checkMove(t *testing.T, step move, legal bool) {
	t.Helper()

	journal := &upgrade.VMJournal{Phase: step.from}
	err := journal.Advance(step.to)

	if legal {
		if err != nil || journal.Phase != step.to {
			t.Errorf("%s to %s: expected the move, got %v and phase %s", step.from, step.to, err, journal.Phase)
		}

		return
	}

	if !errors.Is(err, upgrade.ErrIllegalPhaseMove) {
		t.Errorf("%s to %s: expected a refusal wrapping ErrIllegalPhaseMove, got %v", step.from, step.to, err)

		return
	}

	if !strings.Contains(err.Error(), string(step.from)) || !strings.Contains(err.Error(), string(step.to)) {
		t.Errorf("%s to %s: the error %q does not name both phases", step.from, step.to, err)
	}

	if journal.Phase != step.from {
		t.Errorf("%s to %s: a refused move left the journal in %s", step.from, step.to, journal.Phase)
	}
}

func TestAdvanceRefusesUnknownPhases(t *testing.T) {
	t.Parallel()

	stray := &upgrade.VMJournal{Phase: "melting"}

	err := stray.Advance(upgrade.VMPhaseSwapping)
	if !errors.Is(err, upgrade.ErrIllegalPhaseMove) {
		t.Fatalf("an unknown phase moved forward: %v", err)
	}

	err = stray.Advance(upgrade.VMPhaseUnrecovered)
	if err != nil {
		t.Fatalf("an unknown phase could not be marked unrecovered: %v", err)
	}

	planned := &upgrade.VMJournal{Phase: upgrade.VMPhasePlanned}

	err = planned.Advance("melting")
	if !errors.Is(err, upgrade.ErrIllegalPhaseMove) || planned.Phase != upgrade.VMPhasePlanned {
		t.Fatalf("a move to an unknown phase went through: %v, phase %s", err, planned.Phase)
	}

	var missing *upgrade.VMJournal

	err = missing.Advance(upgrade.VMPhaseStaged)
	if !errors.Is(err, upgrade.ErrIllegalPhaseMove) {
		t.Fatalf("a nil journal moved: %v", err)
	}
}

// The live states a resume can find. Each journal below has two files,
// a and b, whose old and new hashes differ.
const (
	journalCID  = "vm-cid-1"
	recreated   = "vm-cid-2"
	oldA        = "1111111111111111111111111111111111111111111111111111111111111111"
	newA        = "2222222222222222222222222222222222222222222222222222222222222222"
	oldB        = "3333333333333333333333333333333333333333333333333333333333333333"
	newB        = "4444444444444444444444444444444444444444444444444444444444444444"
	strayHash   = "5555555555555555555555555555555555555555555555555555555555555555"
	pathA       = "/var/vcap/data/jobs/redis/abc/config/tls/cert.pem"
	pathB       = "/var/vcap/data/jobs/redis/abc/config/tls/key.pem"
	backupPathA = "/var/vcap/data/certpatch/task-1/b/0"
	backupPathB = "/var/vcap/data/certpatch/task-1/b/1"
)

// resumeJournal builds a two-file journal in the given phase.
func resumeJournal(phase upgrade.VMPhase, cleanup string) *upgrade.VMJournal {
	return &upgrade.VMJournal{
		VM:    "redis/0",
		CID:   journalCID,
		Phase: phase,
		Files: []upgrade.FileEntry{
			{Path: pathA, Role: upgrade.FileRoleCertificate, OldHash: oldA, NewHash: newA, BackupPath: backupPathA, State: upgrade.FileStateBackedUp},
			{Path: pathB, Role: upgrade.FileRoleKey, OldHash: oldB, NewHash: newB, BackupPath: backupPathB, State: upgrade.FileStateBackedUp},
		},
		Cleanup: cleanup,
	}
}

func replacingJournal() *upgrade.VMJournal {
	journal := resumeJournal(upgrade.VMPhaseSwapping, "")
	journal.Files[0].State = upgrade.FileStateReplacing

	return journal
}

func noCIDJournal() *upgrade.VMJournal {
	journal := resumeJournal(upgrade.VMPhaseSwapping, "")
	journal.CID = ""

	return journal
}

func sameHashJournal() *upgrade.VMJournal {
	journal := resumeJournal(upgrade.VMPhaseStaged, "")
	journal.Files[1].NewHash = journal.Files[1].OldHash

	return journal
}

// Shorthands for the live hashes of the two files and their backups.
func bothOld() map[string]string    { return map[string]string{pathA: oldA, pathB: oldB} }
func bothNew() map[string]string    { return map[string]string{pathA: newA, pathB: newB} }
func oldThenNew() map[string]string { return map[string]string{pathA: oldA, pathB: newB} }
func strayLive() map[string]string  { return map[string]string{pathA: strayHash, pathB: oldB} }
func missingB() map[string]string   { return map[string]string{pathA: oldA} }

func noBackups() map[string]string { return map[string]string{} }
func oneBackup() map[string]string { return map[string]string{backupPathA: oldA} }

func bothBackups() map[string]string {
	return map[string]string{backupPathA: oldA, backupPathB: oldB}
}

func driftBackups() map[string]string {
	return map[string]string{backupPathA: oldA, backupPathB: strayHash}
}

func live(cid string, files, backups map[string]string) upgrade.LiveState {
	return upgrade.LiveState{CID: cid, FileHashes: files, BackupHashes: backups}
}

type resumeCase struct {
	name    string
	journal *upgrade.VMJournal
	live    upgrade.LiveState
	want    upgrade.ResumeAction
}

// tableRows has one case per line of the resume table, in its order.
func tableRows() []resumeCase {
	return []resumeCase{
		{"open phase on a recreated VM", resumeJournal(upgrade.VMPhaseSwapping, ""), live(recreated, bothNew(), bothBackups()), upgrade.ResumeMarkReverted},
		{"open phase with a drifted backup", resumeJournal(upgrade.VMPhaseBackedUp, ""), live(journalCID, bothOld(), driftBackups()), upgrade.ResumeUnrecovered},
		{"open phase with a stray file hash", resumeJournal(upgrade.VMPhaseSwapped, ""), live(journalCID, strayLive(), bothBackups()), upgrade.ResumeUnrecovered},
		{"rolling back with every file new", resumeJournal(upgrade.VMPhaseRollingBack, ""), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeRestoreAll},
		{"rolling back with a mix", resumeJournal(upgrade.VMPhaseRollingBack, ""), live(journalCID, oldThenNew(), bothBackups()), upgrade.ResumeCompleteRestore},
		{"rolling back with every file old", resumeJournal(upgrade.VMPhaseRollingBack, ""), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeRestartRollback},
		{"planned with no backups", resumeJournal(upgrade.VMPhasePlanned, ""), live(journalCID, bothOld(), noBackups()), upgrade.ResumeContinueStaging},
		{"staged with no backups", resumeJournal(upgrade.VMPhaseStaged, ""), live(journalCID, bothOld(), noBackups()), upgrade.ResumeContinueStaging},
		{"staged with some backups", resumeJournal(upgrade.VMPhaseStaged, ""), live(journalCID, bothOld(), oneBackup()), upgrade.ResumeContinueBackups},
		{"backed up with every backup", resumeJournal(upgrade.VMPhaseBackedUp, ""), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeContinueBackups},
		{"swapping with every file old", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeCompleteForward},
		{"swapping with a mix", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, oldThenNew(), bothBackups()), upgrade.ResumeCompleteForward},
		{"swapping with every file new", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeRestartForward},
		{"swapped with every file new", resumeJournal(upgrade.VMPhaseSwapped, ""), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeRestartForward},
		{"restarted with every file new", resumeJournal(upgrade.VMPhaseRestarted, ""), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeVerifyAgain},
		{"verified with every file new", resumeJournal(upgrade.VMPhaseVerified, ""), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeVerifyAgain},
		{"restarted with a file old", resumeJournal(upgrade.VMPhaseRestarted, ""), live(journalCID, oldThenNew(), bothBackups()), upgrade.ResumeUnrecovered},
		{"verified with every file old", resumeJournal(upgrade.VMPhaseVerified, ""), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeUnrecovered},
		{"backed up with no backups", resumeJournal(upgrade.VMPhaseBackedUp, ""), live(journalCID, bothOld(), noBackups()), upgrade.ResumeUnrecovered},
		{"done with cleanup pending", resumeJournal(upgrade.VMPhaseDone, upgrade.CleanupPending), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeCleanup},
		{"unrecovered with cleanup failed", resumeJournal(upgrade.VMPhaseUnrecovered, upgrade.CleanupFailed), live(journalCID, strayLive(), driftBackups()), upgrade.ResumeCleanup},
		{"done with cleanup done", resumeJournal(upgrade.VMPhaseDone, upgrade.CleanupDone), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeNothing},
		{"reverted with cleanup pending", resumeJournal(upgrade.VMPhaseReverted, upgrade.CleanupPending), live(recreated, bothOld(), noBackups()), upgrade.ResumeNothing},
	}
}

// guardRows cover rollback, cleanup, the fall-through, and live states
// the table's rows read as unknown.
func guardRows() []resumeCase {
	return []resumeCase{
		{"rolling back, old with backups, is no forward step", resumeJournal(upgrade.VMPhaseRollingBack, ""), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeRestartRollback},
		{"rolling back, every file new, restores", resumeJournal(upgrade.VMPhaseRollingBack, ""), live(journalCID, bothNew(), bothBackups()), upgrade.ResumeRestoreAll},
		{"rolled back with cleanup done ignores hashes", resumeJournal(upgrade.VMPhaseRolledBack, upgrade.CleanupDone), live(recreated, strayLive(), driftBackups()), upgrade.ResumeNothing},
		{"rolled back with an empty cleanup", resumeJournal(upgrade.VMPhaseRolledBack, ""), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeCleanup},
		{"swapping with a replacing file still old", replacingJournal(), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeCompleteForward},
		{"swapping with a backup missing", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, bothOld(), oneBackup()), upgrade.ResumeUnrecovered},
		{"swapping with a mix and a backup missing", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, oldThenNew(), oneBackup()), upgrade.ResumeUnrecovered},
		{"swapping with a mix and no backups", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, oldThenNew(), noBackups()), upgrade.ResumeUnrecovered},
		{"staged with a file already new", resumeJournal(upgrade.VMPhaseStaged, ""), live(journalCID, oldThenNew(), noBackups()), upgrade.ResumeUnrecovered},
		{"swapped with a file old", resumeJournal(upgrade.VMPhaseSwapped, ""), live(journalCID, oldThenNew(), bothBackups()), upgrade.ResumeUnrecovered},
		{"a file missing on the VM", resumeJournal(upgrade.VMPhaseSwapping, ""), live(journalCID, missingB(), bothBackups()), upgrade.ResumeUnrecovered},
		{"the director reports no CID", resumeJournal(upgrade.VMPhaseStaged, ""), live("", bothOld(), noBackups()), upgrade.ResumeRetryLater},
		{"a verified VM the director reports no CID for", resumeJournal(upgrade.VMPhaseVerified, ""), live("", bothNew(), bothBackups()), upgrade.ResumeRetryLater},
		{"a half-swapped VM the director reports no CID for", resumeJournal(upgrade.VMPhaseSwapping, ""), live("", oldThenNew(), bothBackups()), upgrade.ResumeRetryLater},
		{"a journal with no CID", noCIDJournal(), live(journalCID, bothOld(), bothBackups()), upgrade.ResumeUnrecovered},
		{"a journal and a director with no CID", noCIDJournal(), live("", bothOld(), bothBackups()), upgrade.ResumeUnrecovered},
		{"a closed VM the director reports no CID for", resumeJournal(upgrade.VMPhaseDone, upgrade.CleanupDone), live("", bothNew(), bothBackups()), upgrade.ResumeNothing},
		{"a recreate outranks a stray hash", resumeJournal(upgrade.VMPhaseVerified, ""), live(recreated, strayLive(), driftBackups()), upgrade.ResumeMarkReverted},
		{"a journal with no files", &upgrade.VMJournal{CID: journalCID, Phase: upgrade.VMPhasePlanned}, live(journalCID, nil, nil), upgrade.ResumeUnrecovered},
		{"a file whose old and new hashes match", sameHashJournal(), live(journalCID, bothOld(), noBackups()), upgrade.ResumeUnrecovered},
		{"an unknown phase", resumeJournal("melting", ""), live(journalCID, bothOld(), noBackups()), upgrade.ResumeUnrecovered},
	}
}

func TestDecideResume(t *testing.T) {
	t.Parallel()

	for _, row := range append(tableRows(), guardRows()...) {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			got, why := upgrade.DecideResume(row.journal, row.live)
			if got != row.want {
				t.Fatalf("expected %s, got %s (%s)", row.want, got, why)
			}

			if why == "" || strings.Contains(why, ". ") || !strings.HasSuffix(why, ".") {
				t.Fatalf("expected one sentence, got %q", why)
			}

			for _, hash := range []string{oldA, newA, oldB, newB, strayHash} {
				if strings.Contains(why, hash) {
					t.Fatalf("the explanation %q carries a hash", why)
				}
			}
		})
	}
}

// Every action the table takes on an open phase leads somewhere Advance
// allows, so the executor can always record the outcome. An action may
// first finish the work of the phase it resumes in, so staying put counts.
func TestDecideResumeActionsHaveLegalMoves(t *testing.T) {
	t.Parallel()

	ends := map[upgrade.ResumeAction][]upgrade.VMPhase{
		upgrade.ResumeMarkReverted:    {upgrade.VMPhaseReverted},
		upgrade.ResumeUnrecovered:     {upgrade.VMPhaseUnrecovered},
		upgrade.ResumeContinueStaging: {upgrade.VMPhaseStaged},
		upgrade.ResumeContinueBackups: {upgrade.VMPhaseBackedUp, upgrade.VMPhaseSwapping},
		upgrade.ResumeCompleteForward: {upgrade.VMPhaseSwapped},
		upgrade.ResumeRestartForward:  {upgrade.VMPhaseSwapped, upgrade.VMPhaseRestarted},
		upgrade.ResumeVerifyAgain:     {upgrade.VMPhaseVerified, upgrade.VMPhaseDone},
		upgrade.ResumeRestoreAll:      {upgrade.VMPhaseRolledBack, upgrade.VMPhaseUnrecovered},
		upgrade.ResumeCompleteRestore: {upgrade.VMPhaseRolledBack, upgrade.VMPhaseUnrecovered},
		upgrade.ResumeRestartRollback: {upgrade.VMPhaseRolledBack, upgrade.VMPhaseUnrecovered},
		upgrade.ResumeRetryLater:      openPhases(),
	}

	lives := []upgrade.LiveState{
		live(journalCID, bothOld(), noBackups()), live(journalCID, bothOld(), oneBackup()),
		live(journalCID, bothOld(), bothBackups()), live(journalCID, oldThenNew(), bothBackups()),
		live(journalCID, bothNew(), bothBackups()), live(recreated, bothNew(), bothBackups()),
		live("", bothNew(), bothBackups()),
	}

	for _, phase := range openPhases() {
		for _, state := range lives {
			action, _ := upgrade.DecideResume(resumeJournal(phase, ""), state)

			targets, ok := ends[action]
			if !ok {
				t.Fatalf("%s gave %s, which no open phase should", phase, action)
			}

			if !reachable(phase, targets) {
				t.Fatalf("%s gave %s, but none of %v is a legal move from it", phase, action, targets)
			}
		}
	}
}

// reachable reports whether the phase is one of the targets or moves to
// one of them in a single legal step.
func reachable(phase upgrade.VMPhase, targets []upgrade.VMPhase) bool {
	for _, target := range targets {
		if target == phase || (&upgrade.VMJournal{Phase: phase}).Advance(target) == nil {
			return true
		}
	}

	return false
}

func TestDecideResumeNilJournal(t *testing.T) {
	t.Parallel()

	got, why := upgrade.DecideResume(nil, upgrade.LiveState{})
	if got != upgrade.ResumeUnrecovered || why == "" {
		t.Fatalf("expected unrecovered for a nil journal, got %s (%q)", got, why)
	}
}

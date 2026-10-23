package upgrade

import (
	"errors"
	"fmt"
)

// ErrIllegalPhaseMove is wrapped by every refusal from VMJournal.Advance.
var ErrIllegalPhaseMove = errors.New("illegal VM phase move")

// phaseSuccessors returns the moves the patch state diagram draws out of
// a phase. Advance also lets any open phase move to unrecovered or
// reverted, which a resume needs and the diagram leaves out.
func phaseSuccessors(phase VMPhase) []VMPhase {
	switch phase {
	case VMPhasePlanned:
		return []VMPhase{VMPhaseStaged, VMPhaseUnrecovered}
	case VMPhaseStaged:
		return []VMPhase{VMPhaseBackedUp}
	case VMPhaseBackedUp:
		return []VMPhase{VMPhaseSwapping}
	case VMPhaseSwapping:
		return []VMPhase{VMPhaseSwapped, VMPhaseRollingBack}
	case VMPhaseSwapped:
		return []VMPhase{VMPhaseRestarted, VMPhaseRollingBack}
	case VMPhaseRestarted:
		return []VMPhase{VMPhaseVerified, VMPhaseRollingBack}
	case VMPhaseVerified:
		return []VMPhase{VMPhaseDone, VMPhaseRollingBack}
	case VMPhaseRollingBack:
		return []VMPhase{VMPhaseRolledBack, VMPhaseUnrecovered}
	case VMPhaseDone, VMPhaseRolledBack, VMPhaseUnrecovered, VMPhaseReverted:
		return nil
	default:
		return nil
	}
}

// closedPhase reports whether a resume leaves the VM's phase alone. Any
// other value, an unknown one included, is open.
func closedPhase(phase VMPhase) bool {
	switch phase {
	case VMPhaseDone, VMPhaseRolledBack, VMPhaseUnrecovered, VMPhaseReverted:
		return true
	case VMPhasePlanned, VMPhaseStaged, VMPhaseBackedUp, VMPhaseSwapping,
		VMPhaseSwapped, VMPhaseRestarted, VMPhaseVerified, VMPhaseRollingBack:
		return false
	default:
		return false
	}
}

// Advance moves the journal to the given phase when the state diagram
// allows it, or when it marks an open phase unrecovered or reverted. It
// refuses every other move, a move to the phase the journal is already
// in among them, and leaves the journal unchanged.
func (j *VMJournal) Advance(target VMPhase) error {
	if j == nil {
		return fmt.Errorf("%w: no journal to move to %s", ErrIllegalPhaseMove, target)
	}

	if !closedPhase(j.Phase) && (target == VMPhaseUnrecovered || target == VMPhaseReverted) {
		j.Phase = target

		return nil
	}

	for _, next := range phaseSuccessors(j.Phase) {
		if next == target {
			j.Phase = target

			return nil
		}
	}

	return fmt.Errorf("%w: %s to %s", ErrIllegalPhaseMove, j.Phase, target)
}

// LiveState is what a resume finds on the VM. FileHashes is keyed by
// FileEntry.Path and BackupHashes by FileEntry.BackupPath, the path the
// patch script's check names when a backup drifts. A file or backup that
// is missing has no key. An empty CID means the director's answer did
// not name the VM, so its identity is unknown rather than changed.
type LiveState struct {
	CID          string
	FileHashes   map[string]string
	BackupHashes map[string]string
}

// ResumeAction is what a resume does with one VM.
type ResumeAction string

// The resume actions.
const (
	ResumeMarkReverted    ResumeAction = "mark_reverted"
	ResumeRetryLater      ResumeAction = "retry_later"
	ResumeUnrecovered     ResumeAction = "unrecovered"
	ResumeContinueStaging ResumeAction = "continue_staging"
	ResumeContinueBackups ResumeAction = "continue_backups"
	ResumeCompleteForward ResumeAction = "complete_forward"
	ResumeRestartForward  ResumeAction = "restart_forward"
	ResumeVerifyAgain     ResumeAction = "verify_again"
	ResumeRestoreAll      ResumeAction = "restore_all"
	ResumeCompleteRestore ResumeAction = "complete_restore"
	ResumeRestartRollback ResumeAction = "restart_rollback"
	ResumeCleanup         ResumeAction = "cleanup"
	ResumeNothing         ResumeAction = "nothing"
)

// liveTally counts how the journal's files compare with what is on the
// VM. A file whose journal hashes are empty or equal can be neither old
// nor new, so it counts as unknown.
type liveTally struct {
	files, old, new, backups int
	// unknown and drifted name the first file and backup that fit
	// neither hash.
	unknown, drifted string
}

func tallyLive(journal *VMJournal, live LiveState) liveTally {
	tally := liveTally{files: len(journal.Files)}

	for _, file := range journal.Files {
		if file.BackupPath != "" {
			if hash, ok := live.BackupHashes[file.BackupPath]; ok {
				tally.backups++

				if hash != file.OldHash && tally.drifted == "" {
					tally.drifted = file.BackupPath
				}
			}
		}

		hash, ok := live.FileHashes[file.Path]

		switch {
		case !ok || file.OldHash == "" || file.NewHash == "" || file.OldHash == file.NewHash:
			if tally.unknown == "" {
				tally.unknown = file.Path
			}
		case hash == file.OldHash:
			tally.old++
		case hash == file.NewHash:
			tally.new++
		default:
			if tally.unknown == "" {
				tally.unknown = file.Path
			}
		}
	}

	return tally
}

func (t liveTally) allOld() bool { return t.files > 0 && t.old == t.files }
func (t liveTally) allNew() bool { return t.files > 0 && t.new == t.files }
func (t liveTally) mixed() bool  { return t.old > 0 && t.new > 0 && t.old+t.new == t.files }

// DecideResume picks what a resume does with one VM from its journal and
// what the VM holds now. The rows are checked in order and the first that
// matches wins. A combination no row names leaves an open VM unrecovered,
// so a resume never guesses a forward step. Only a different, non-empty
// CID marks a VM reverted. A director answer with no CID leaves the VM in
// its phase for a later resume, and a journal with no CID matches nothing
// and leaves it unrecovered. The string explains the choice in one
// sentence for the task record, and it never carries a hash.
func DecideResume(journal *VMJournal, live LiveState) (ResumeAction, string) {
	if journal == nil {
		return ResumeUnrecovered, "There is no journal for this VM, so nothing about its files is known."
	}

	if closedPhase(journal.Phase) {
		if journal.Phase != VMPhaseReverted && journal.Cleanup != CleanupDone {
			return ResumeCleanup, fmt.Sprintf("The VM is %s and its staging and backup files are not yet removed.", journal.Phase)
		}

		return ResumeNothing, fmt.Sprintf("The VM is %s and has nothing left to do.", journal.Phase)
	}

	if journal.CID == "" {
		return ResumeUnrecovered, fmt.Sprintf("The journal names no CID for the VM, so a VM left %s cannot be matched to the one the director reports.", journal.Phase)
	}

	if live.CID == "" {
		return ResumeRetryLater, fmt.Sprintf("The director did not name the VM's CID, so the VM stays %s until a later resume can confirm which VM it is.", journal.Phase)
	}

	if live.CID != journal.CID {
		return ResumeMarkReverted, fmt.Sprintf("The VM's CID changed while it was %s, so BOSH recreated it with the old files.", journal.Phase)
	}

	tally := tallyLive(journal, live)

	if tally.drifted != "" {
		return ResumeUnrecovered, fmt.Sprintf("The backup %s no longer holds the old content, so the VM was left %s with nothing overwritten.", tally.drifted, journal.Phase)
	}

	if tally.unknown != "" {
		return ResumeUnrecovered, fmt.Sprintf("The file %s is missing or holds neither the old nor the new content, so the VM was left %s with nothing overwritten.", tally.unknown, journal.Phase)
	}

	if action, why, ok := decideOpen(journal.Phase, tally); ok {
		return action, why
	}

	return ResumeUnrecovered, fmt.Sprintf("The VM was %s with %d of %d files new and %d backups present, which no resume rule covers.", journal.Phase, tally.new, tally.files, tally.backups)
}

// decideOpen applies the rows for an open phase whose files all hold the
// old or the new content. It reports false when no row matches.
//
//nolint:cyclop // one case per row of the resume table keeps the order visible
func decideOpen(phase VMPhase, tally liveTally) (ResumeAction, string, bool) {
	everyBackup := tally.backups == tally.files

	switch {
	case phase == VMPhaseRollingBack && tally.allNew():
		return ResumeRestoreAll, "The rollback stopped before any file was restored, so every file is restored from its backup.", true
	case phase == VMPhaseRollingBack && tally.mixed():
		return ResumeCompleteRestore, "The rollback stopped part way, so the files still new are restored from their backups.", true
	case phase == VMPhaseRollingBack && tally.allOld():
		return ResumeRestartRollback, "The restore finished before the stop, so the touched processes restart and the gate runs.", true
	case (phase == VMPhasePlanned || phase == VMPhaseStaged) && tally.allOld() && tally.backups == 0:
		return ResumeContinueStaging, "No file was changed and no backup exists, so the patch continues from staging.", true
	case (phase == VMPhaseStaged || phase == VMPhaseBackedUp) && tally.allOld() && tally.backups > 0:
		return ResumeContinueBackups, "No file was changed, so the patch keeps its backups, makes any missing ones, and continues to the first replacement.", true
	case phase == VMPhaseSwapping && tally.allOld() && everyBackup:
		return ResumeCompleteForward, "The swap stopped before any file changed and every backup is present, so the replacements are completed forward.", true
	case phase == VMPhaseSwapping && tally.mixed() && everyBackup:
		return ResumeCompleteForward, "The swap stopped part way and every backup is present, so the remaining replacements are completed forward.", true
	case (phase == VMPhaseSwapping || phase == VMPhaseSwapped) && tally.allNew():
		return ResumeRestartForward, "Every file holds the new content, so the touched processes restart and the patch continues.", true
	case (phase == VMPhaseRestarted || phase == VMPhaseVerified) && tally.allNew():
		return ResumeVerifyAgain, "Every file holds the new content, so the targets are verified again before the gate runs.", true
	case (phase == VMPhaseRestarted || phase == VMPhaseVerified) && tally.old > 0:
		return ResumeUnrecovered, fmt.Sprintf("The VM was %s but a file holds the old content again, so it was left with nothing overwritten.", phase), true
	default:
		return "", "", false
	}
}

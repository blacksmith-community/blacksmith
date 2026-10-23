package certinventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/credhub"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

// Vault paths of the divergence records. The index document lists every
// instance that has a record, and each record lives under it by instance ID.
const divergenceVaultPath = "certificate-divergence"

// The state of one VM in a divergence entry.
const (
	DivergentVMApplied  = "applied"
	DivergentVMOld      = "old"
	DivergentVMReverted = "reverted"
	DivergentVMUnknown  = "unknown"
)

// DivergenceConverging marks an entry whose converge deploy is under way.
const DivergenceConverging = "converging"

const (
	// divergenceTimeout bounds one Get, List, or Update, its read-back
	// included.
	divergenceTimeout = 10 * time.Second
	// reconcileVersionLimit is how many of a variable's newest versions
	// reconciliation reads.
	reconcileVersionLimit = 20
)

// ErrInvalidInstanceID is returned for an instance ID that cannot name a
// Vault path of its own.
var ErrInvalidInstanceID = errors.New("invalid instance ID for a divergence record")

// Divergence records, for one instance, each certificate variable that a
// patch changed on the VMs while BOSH still records an older version as
// deployed. Revision rises by one on every change, so a writer can tell its
// own write from an older one.
type Divergence struct {
	InstanceID string              `json:"instance_id"`
	Variables  []DivergentVariable `json:"variables"`
	Revision   int64               `json:"revision"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

// DivergentVariable is one patched variable. Name is the variable's
// absolute CredHub name, the same name a certificate's Key carries.
type DivergentVariable struct {
	Name             string                 `json:"name"`
	BOSHVersionID    string                 `json:"bosh_version_id"`    // what BOSH records as deployed
	AppliedVersionID string                 `json:"applied_version_id"` // what the patch wrote
	VMs              map[string]DivergentVM `json:"vms"`                // keyed by VM CID
	TaskID           string                 `json:"task_id"`
	Since            time.Time              `json:"since"`
	State            string                 `json:"state"` // "unconverged", "reverted", "converging"
	UpdatedAt        time.Time              `json:"updated_at"`
}

// DivergentVM is what one VM holds of a patched variable.
type DivergentVM struct {
	State     string    `json:"state"` // "applied", "old", "reverted", or "unknown"
	UpdatedAt time.Time `json:"updated_at"`
}

// Clone returns a deep copy of d.
func (d *Divergence) Clone() *Divergence {
	if d == nil {
		return nil
	}

	out := *d
	out.Variables = make([]DivergentVariable, len(d.Variables))

	for i, v := range d.Variables {
		out.Variables[i] = v.clone()
	}

	return &out
}

func (v DivergentVariable) clone() DivergentVariable {
	if v.VMs != nil {
		vms := make(map[string]DivergentVM, len(v.VMs))
		for cid, vm := range v.VMs {
			vms[cid] = vm
		}

		v.VMs = vms
	}

	return v
}

// entry returns the record's entry for a variable, or nil.
func (d *Divergence) entry(name string) *DivergentVariable {
	if d == nil {
		return nil
	}

	for i := range d.Variables {
		if d.Variables[i].Name == name {
			return &d.Variables[i]
		}
	}

	return nil
}

// DivergenceRefOf returns the certificate field for one variable of the
// record, or nil when the record has no entry for it. The state is
// reverted when the entry or any of its VMs is reverted, and unconverged
// otherwise.
func DivergenceRefOf(d *Divergence, variable string) *DivergenceRef {
	e := d.entry(variable)
	if e == nil {
		return nil
	}

	state := DivergenceUnconverged
	if e.State == DivergenceReverted {
		state = DivergenceReverted
	}

	for _, vm := range e.VMs {
		if vm.State == DivergentVMReverted {
			state = DivergenceReverted
		}
	}

	return &DivergenceRef{State: state, TaskID: e.TaskID, Since: e.Since}
}

// VersionLookup is what reconciliation reads from CredHub: each version's
// creation time and each version's public certificate. *credhub.Client
// satisfies it.
type VersionLookup interface {
	CredentialVersions(ctx context.Context, name string, limit int) ([]credhub.CredentialVersion, error)
	CertificateVersion(ctx context.Context, versionID string) (*credhub.CertificatePublic, error)
}

var _ VersionLookup = (*credhub.Client)(nil)

// ReconcileSources is what reconciliation reads beyond the scan. A nil
// Versions reconciles nothing, and a nil Log logs nothing.
type ReconcileSources struct {
	Versions VersionLookup
	Log      logger.Logger
}

func (src ReconcileSources) log() logger.Logger {
	if isNil(src.Log) {
		return logger.NewNoOpLogger()
	}

	return src.Log
}

// DivergenceStore keeps the divergence records in Vault. Every change goes
// through Update, which holds the instance's lock across its read, change,
// and write, so the runner and reconciliation never lose each other's
// changes. It is safe for concurrent use.
type DivergenceStore struct {
	vault interfaces.Vault
	now   func() time.Time

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex

	// indexMu is held across each read, change, and write of the index. An
	// instance lock is always taken before it.
	indexMu sync.Mutex

	generation atomic.Uint64

	// scansMu guards reconciled, the start of the latest scan reconciled
	// with each instance's record. It lives in memory, so it orders the
	// scans of this broker process.
	scansMu    sync.Mutex
	reconciled map[string]time.Time
}

// NewDivergenceStore returns a store over v. A nil now means time.Now.
func NewDivergenceStore(v interfaces.Vault, now func() time.Time) *DivergenceStore {
	if now == nil {
		now = time.Now
	}

	return &DivergenceStore{vault: v, now: now, locks: map[string]*sync.Mutex{}, reconciled: map[string]time.Time{}}
}

// Generation rises on every confirmed change, so a reader can rebuild what
// it derives from the records only when one changed.
func (s *DivergenceStore) Generation() uint64 { return s.generation.Load() }

func divergenceRecordPath(instanceID string) string {
	return divergenceVaultPath + "/" + instanceID
}

// validInstanceID refuses an ID that would name another path.
func validInstanceID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("%w: %q", ErrInvalidInstanceID, id)
	}

	return nil
}

func (s *DivergenceStore) lockFor(id string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()

	lock, ok := s.locks[id]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[id] = lock
	}

	return lock
}

// Get reads one instance's record.
func (s *DivergenceStore) Get(ctx context.Context, instanceID string) (*Divergence, bool, error) {
	if err := validInstanceID(instanceID); err != nil {
		return nil, false, err
	}

	ctx, cancel := context.WithTimeout(ctx, divergenceTimeout)
	defer cancel()

	return s.read(ctx, instanceID)
}

func (s *DivergenceStore) read(ctx context.Context, instanceID string) (*Divergence, bool, error) {
	path := divergenceRecordPath(instanceID)

	var d Divergence

	found, err := s.vault.Get(ctx, path, &d)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", path, err)
	}

	if !found {
		return nil, false, nil
	}

	return &d, true, nil
}

// List reads every record the index names, in instance ID order. A record
// the index names but Vault does not hold is skipped, since the index is
// written before a record and cleaned after one is deleted. Any failed read
// fails the whole list, so a record is never silently left out.
func (s *DivergenceStore) List(ctx context.Context) ([]*Divergence, error) {
	ctx, cancel := context.WithTimeout(ctx, divergenceTimeout)
	defer cancel()

	index, _, err := s.readIndex(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(index))
	for id := range index {
		if validInstanceID(id) == nil {
			ids = append(ids, id)
		}
	}

	sort.Strings(ids)

	out := make([]*Divergence, 0, len(ids))

	for _, id := range ids {
		d, found, err := s.read(ctx, id)
		if err != nil {
			return nil, err
		}

		if found {
			out = append(out, d)
		}
	}

	return out, nil
}

func (s *DivergenceStore) readIndex(ctx context.Context) (map[string]interface{}, bool, error) {
	var index map[string]interface{}

	found, err := s.vault.Get(ctx, divergenceVaultPath, &index)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", divergenceVaultPath, err)
	}

	if index == nil {
		index = map[string]interface{}{}
	}

	return index, found, nil
}

// Update changes one instance's record. It holds the instance's lock for
// the whole read, change, and write. fn gets a deep copy of the current
// record, or nil when there is none, and returns the new record, or nil to
// delete it. An error from fn is returned as it is and nothing is written.
//
// Every entry and VM entry that fn changed gets the time of the change as
// its UpdatedAt, so a scan that began earlier leaves it alone. A result
// with the same content as the stored record writes nothing. Otherwise the
// record is written with Revision one higher. A write that returns an
// error may still have landed, so the record is read back, and the write
// counts only when the stored revision is the one written. A failure to
// write or confirm the record, or to confirm its delete, wraps
// ErrSaveUnconfirmed. A failure before the record's write or delete was
// attempted, such as the first read or the index update, leaves the stored
// record as it was, so it is returned as a definite error.
func (s *DivergenceStore) Update(ctx context.Context, instanceID string, fn func(cur *Divergence) (*Divergence, error)) error {
	if err := validInstanceID(instanceID); err != nil {
		return err
	}

	lock := s.lockFor(instanceID)
	lock.Lock()
	defer lock.Unlock()

	// A change must land, or be known not to, even when the caller's
	// context ends part way through.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), divergenceTimeout)
	defer cancel()

	cur, found, err := s.read(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("the divergence record of %s was not changed: %w", instanceID, err)
	}

	next, err := fn(cur.Clone())
	if err != nil {
		return err
	}

	if next == nil {
		if !found {
			return nil
		}

		return s.remove(ctx, instanceID)
	}

	next = next.Clone()
	next.InstanceID = instanceID
	now := s.now()

	stampChanges(cur, next, now)

	if found && sameRecord(cur, next) {
		return nil
	}

	next.Revision = 1
	if found {
		next.Revision = cur.Revision + 1
	}

	next.UpdatedAt = now

	if err := s.addToIndex(ctx, instanceID); err != nil {
		return err
	}

	if err := s.write(ctx, next); err != nil {
		return err
	}

	s.generation.Add(1)

	return nil
}

// stampChanges gives every entry of next that differs from cur, and every
// VM entry whose state differs, the time of this change.
func stampChanges(cur, next *Divergence, now time.Time) {
	for i := range next.Variables {
		v := &next.Variables[i]
		prev := cur.entry(v.Name)

		if prev == nil || !sameEntry(*prev, *v) {
			v.UpdatedAt = now
		}

		for cid, vm := range v.VMs {
			var before DivergentVM

			ok := false
			if prev != nil {
				before, ok = prev.VMs[cid]
			}

			if !ok || before.State != vm.State {
				vm.UpdatedAt = now
				v.VMs[cid] = vm
			}
		}
	}
}

// sameEntry compares two entries without their times of change. Entries
// that cannot be marshalled count as different, so a needed write is never
// skipped.
func sameEntry(a, b DivergentVariable) bool {
	rawA, errA := entryJSON(a)
	rawB, errB := entryJSON(b)

	return errA == nil && errB == nil && bytes.Equal(rawA, rawB)
}

func entryJSON(v DivergentVariable) ([]byte, error) {
	v = v.clone()
	v.UpdatedAt = time.Time{}

	for cid, vm := range v.VMs {
		vm.UpdatedAt = time.Time{}
		v.VMs[cid] = vm
	}

	return json.Marshal(v)
}

// sameRecord compares two records without their revisions and times of
// change. Records that cannot be marshalled count as different, so Update
// writes and the write's own failure surfaces.
func sameRecord(a, b *Divergence) bool {
	strip := func(d *Divergence) ([]byte, error) {
		c := d.Clone()
		c.Revision, c.UpdatedAt = 0, time.Time{}

		return json.Marshal(c)
	}

	rawA, errA := strip(a)
	rawB, errB := strip(b)

	return errA == nil && errB == nil && bytes.Equal(rawA, rawB)
}

// write stores the record and confirms it as Update describes.
func (s *DivergenceStore) write(ctx context.Context, d *Divergence) error {
	path := divergenceRecordPath(d.InstanceID)

	writeErr := s.vault.Put(ctx, path, d)
	if writeErr == nil {
		return nil
	}

	stored, found, err := s.read(ctx, d.InstanceID)
	if err != nil {
		return fmt.Errorf("%w: the write of %s failed (%w) and reading it back failed: %w", ErrSaveUnconfirmed, path, writeErr, err)
	}

	if !found || stored.Revision != d.Revision {
		return fmt.Errorf("%w: the write of %s at revision %d did not land: %w", ErrSaveUnconfirmed, path, d.Revision, writeErr)
	}

	return nil
}

// remove deletes the record and confirms it is gone, then drops the
// instance from the index. A failure to clean the index only leaves it
// naming a record that List skips.
func (s *DivergenceStore) remove(ctx context.Context, instanceID string) error {
	path := divergenceRecordPath(instanceID)

	if deleteErr := s.vault.Delete(ctx, path); deleteErr != nil {
		_, found, err := s.read(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("%w: the delete of %s failed (%w) and reading it back failed: %w", ErrSaveUnconfirmed, path, deleteErr, err)
		}

		if found {
			return fmt.Errorf("%w: the delete of %s did not land: %w", ErrSaveUnconfirmed, path, deleteErr)
		}
	}

	s.generation.Add(1)

	_ = s.removeFromIndex(ctx, instanceID)

	return nil
}

// addToIndex makes sure the index names the instance before its record is
// written, so List never misses a record that landed. An index that cannot
// be read is never overwritten. The record has not been written when it
// fails, so every failure is definite.
func (s *DivergenceStore) addToIndex(ctx context.Context, instanceID string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	index, _, err := s.readIndex(ctx)
	if err != nil {
		return fmt.Errorf("the divergence record of %s was not written: %w", instanceID, err)
	}

	if _, named := index[instanceID]; named {
		return nil
	}

	index[instanceID] = true

	writeErr := s.vault.Put(ctx, divergenceVaultPath, index)
	if writeErr == nil {
		return nil
	}

	back, _, err := s.readIndex(ctx)
	if err != nil {
		return fmt.Errorf("the divergence record of %s was not written: the write of %s failed (%w) and reading it back failed: %w", instanceID, divergenceVaultPath, writeErr, err)
	}

	if _, named := back[instanceID]; !named {
		return fmt.Errorf("the divergence record of %s was not written: %s does not name it: %w", instanceID, divergenceVaultPath, writeErr)
	}

	return nil
}

func (s *DivergenceStore) removeFromIndex(ctx context.Context, instanceID string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()

	index, found, err := s.readIndex(ctx)
	if err != nil {
		return err
	}

	if _, named := index[instanceID]; !found || !named {
		return nil
	}

	delete(index, instanceID)

	return s.vault.Put(ctx, divergenceVaultPath, index)
}

// ObservedLeaf is the leaf one listener served in a scan, and the VM it
// was served from.
type ObservedLeaf struct {
	VMCID       string
	Fingerprint string
	At          time.Time
}

// ReconcileInput is what one instance scan tells reconciliation. Deployed
// maps a variable's absolute CredHub name to the version BOSH records as
// deployed, Expected maps it to the listeners that should serve it, and
// Observed maps a listener locator, in the "<group>/<index>:<port>" form
// of ExpectedListener, to what it served. The version maps hold the public
// fingerprint and the CredHub creation time of each version read.
type ReconcileInput struct {
	ScanStartedAt      time.Time
	Deployed           map[string]string
	Expected           map[string][]ExpectedListener
	Observed           map[string]ObservedLeaf
	VersionFingerprint map[string]string
	VersionCreated     map[string]time.Time
}

// fresh reports whether the scan itself saw the observation.
func (in ReconcileInput) fresh(o ObservedLeaf) bool {
	return !o.At.IsZero() && !o.At.Before(in.ScanStartedAt) && o.Fingerprint != ""
}

// notBeforeScan reports whether a change at t may have happened after the
// scan read what it read. A change stamped at the scan's own start counts,
// since the scan cannot be shown to have seen it.
func (in ReconcileInput) notBeforeScan(t time.Time) bool {
	return !t.Before(in.ScanStartedAt)
}

// created returns a version's creation time, and false when it is unknown.
func (in ReconcileInput) created(id string) (time.Time, bool) {
	t, ok := in.VersionCreated[id]

	return t, ok && id != "" && !t.IsZero()
}

// Reconcile returns d updated from one scan, or nil when every entry
// cleared. It never changes d. It leaves alone any entry or VM entry that
// changed after the scan began, and it ignores any observation from
// before the scan began.
//
// A VM marked applied becomes reverted when the scan saw it serve a
// version that CredHub created before the applied one. An entry clears
// when BOSH records the applied version, or one CredHub created later, as
// deployed, and every expected listener served that version in this scan.
// An entry with no expected listener clears on the deployed version alone.
// Version IDs are never compared for order, so a missing creation time
// keeps the entry as it is.
func Reconcile(d *Divergence, in ReconcileInput) *Divergence {
	if d == nil {
		return nil
	}

	out := d.Clone()
	byFingerprint := map[string][]string{}

	for id, fp := range in.VersionFingerprint {
		if fp != "" {
			byFingerprint[fp] = append(byFingerprint[fp], id)
		}
	}

	kept := make([]DivergentVariable, 0, len(out.Variables))

	for _, v := range out.Variables {
		if in.notBeforeScan(v.UpdatedAt) {
			kept = append(kept, v)

			continue
		}

		markReverted(&v, in, byFingerprint)

		if clears(v, in) {
			continue
		}

		kept = append(kept, v)
	}

	if len(kept) == 0 {
		return nil
	}

	out.Variables = kept

	return out
}

// markReverted marks each applied VM of the entry that served an older
// version than the applied one.
func markReverted(v *DivergentVariable, in ReconcileInput, byFingerprint map[string][]string) {
	applied, ok := in.created(v.AppliedVersionID)
	if !ok {
		return
	}

	for cid, vm := range v.VMs {
		if vm.State != DivergentVMApplied || in.notBeforeScan(vm.UpdatedAt) {
			continue
		}

		for _, l := range in.Expected[v.Name] {
			o, seen := in.Observed[l.Locator]
			if !seen || o.VMCID != cid || !in.fresh(o) {
				continue
			}

			if !olderThan(byFingerprint[o.Fingerprint], applied, in) {
				continue
			}

			vm.State, vm.UpdatedAt = DivergentVMReverted, o.At
			v.VMs[cid] = vm

			if v.State == DivergenceUnconverged || v.State == "" {
				v.State = DivergenceReverted
			}

			break
		}
	}
}

// olderThan reports whether the served fingerprint names known versions
// that CredHub all created before applied. No match, or a version whose
// creation time is unknown, is not evidence of a revert.
func olderThan(ids []string, applied time.Time, in ReconcileInput) bool {
	if len(ids) == 0 {
		return false
	}

	for _, id := range ids {
		t, ok := in.created(id)
		if !ok || !t.Before(applied) {
			return false
		}
	}

	return true
}

// clears reports whether BOSH now deploys the applied version, or a later
// one, and every expected listener served it in this scan. An entry with a
// VM entry that changed after the scan began never clears, because the
// scan may not have seen that change.
func clears(v DivergentVariable, in ReconcileInput) bool {
	for _, vm := range v.VMs {
		if in.notBeforeScan(vm.UpdatedAt) {
			return false
		}
	}

	deployed := in.Deployed[v.Name]
	if deployed == "" || v.AppliedVersionID == "" {
		return false
	}

	if deployed != v.AppliedVersionID {
		dt, okD := in.created(deployed)
		at, okA := in.created(v.AppliedVersionID)

		if !okD || !okA || !dt.After(at) {
			return false
		}
	}

	listeners := in.Expected[v.Name]
	if len(listeners) == 0 {
		return true
	}

	want := in.VersionFingerprint[deployed]
	if want == "" {
		return false
	}

	for _, l := range listeners {
		o, seen := in.Observed[l.Locator]
		if !seen || !in.fresh(o) || o.Fingerprint != want {
			return false
		}
	}

	return true
}

// ReconcileScan reconciles the instance's record with one of its scans.
// The CredHub reads happen before Update takes the instance's lock, and
// Reconcile then runs inside Update against the record as it stands, so a
// change the runner made after the scan began survives. With a nil
// versions it changes nothing, because every comparison needs a creation
// time. A failed scan, the broker scan, an instance without a record, and
// a scan that began no later than the last one reconciled with the record
// change nothing either.
func (s *DivergenceStore) ReconcileScan(ctx context.Context, scan InstanceScan, src ReconcileSources) error {
	if isNil(src.Versions) || scan.Err != "" || scan.ScannedAt.IsZero() || scan.Ref.ID == BrokerScanID {
		return nil
	}

	if !s.newerScan(scan.Ref.ID, scan.ScannedAt, false) {
		return nil
	}

	cur, found, err := s.Get(ctx, scan.Ref.ID)
	if err != nil || !found {
		return err
	}

	in := reconcileInput(ctx, cur, scan, src)

	return s.Update(ctx, scan.Ref.ID, func(d *Divergence) (*Divergence, error) {
		// A newer scan reconciled while this one read CredHub, so this
		// one's view is older than the record's.
		if !s.newerScan(scan.Ref.ID, scan.ScannedAt, true) {
			return d, nil
		}

		return Reconcile(d, in), nil
	})
}

// newerScan reports whether a scan that began at start is newer than the
// last one reconciled with the instance's record. With claim, a newer scan
// becomes the last one, which Update's callback does under the instance's
// lock, so two scans of one instance never both pass. A scan that claims
// and then fails to write leaves the claim, and the next scan tries again.
func (s *DivergenceStore) newerScan(instanceID string, start time.Time, claim bool) bool {
	s.scansMu.Lock()
	defer s.scansMu.Unlock()

	if last, ok := s.reconciled[instanceID]; ok && !start.After(last) {
		return false
	}

	if claim {
		s.reconciled[instanceID] = start
	}

	return true
}

// reconcileInput builds the input for the record from the scan and CredHub.
func reconcileInput(ctx context.Context, d *Divergence, scan InstanceScan, src ReconcileSources) ReconcileInput {
	in := ReconcileInput{
		ScanStartedAt:      scan.ScannedAt,
		Deployed:           map[string]string{},
		Expected:           map[string][]ExpectedListener{},
		Observed:           observedLeaves(scan),
		VersionFingerprint: map[string]string{},
		VersionCreated:     map[string]time.Time{},
	}

	for _, ch := range scan.Material.CredHub {
		if ch.Versions.DeployedVersionID != "" {
			in.Deployed[ch.Name] = ch.Versions.DeployedVersionID
		}
	}

	for _, el := range scan.Material.Expected {
		if el.Key != "" {
			in.Expected[el.Key] = append(in.Expected[el.Key], el)
		}
	}

	for _, v := range d.Variables {
		readVersions(ctx, v, &in, src.Versions, src.log())
	}

	return in
}

// readVersions fills the creation times of the variable's newest versions,
// and the fingerprints of the versions reconciliation compares against:
// the applied and the deployed version, and then older versions, newest
// first, only until every version an applied VM served is named. A failed
// read is logged with the variable or version it was for and leaves that
// time or fingerprint unknown, which keeps the entry as it is.
func readVersions(ctx context.Context, v DivergentVariable, in *ReconcileInput, versions VersionLookup, log logger.Logger) {
	list, err := within(ctx, CredHubCallTimeout, func(c context.Context) ([]credhub.CredentialVersion, error) {
		return versions.CredentialVersions(c, v.Name, reconcileVersionLimit)
	})
	if err != nil {
		log.Warnf("certificate divergence: the CredHub versions of %s could not be read, so its record stays as it is: %v", v.Name, err)
	} else {
		for _, cv := range list {
			if cv.ID != "" && !cv.CreatedAt.IsZero() {
				in.VersionCreated[cv.ID] = cv.CreatedAt
			}
		}
	}

	fingerprint := func(id string) {
		if _, done := in.VersionFingerprint[id]; done || id == "" {
			return
		}

		pub, err := within(ctx, CredHubCallTimeout, func(c context.Context) (*credhub.CertificatePublic, error) {
			return versions.CertificateVersion(c, id)
		})
		if err != nil {
			log.Warnf("certificate divergence: CredHub version %s of %s could not be read: %v", id, v.Name, err)

			return
		}

		if pub == nil {
			return
		}

		certs, _ := ParsePEMCertificates([]byte(pub.Certificate))
		if len(certs) == 0 {
			return
		}

		in.VersionFingerprint[id] = Fingerprint(certs[0])

		if _, known := in.VersionCreated[id]; !known && !pub.CreatedAt.IsZero() {
			in.VersionCreated[id] = pub.CreatedAt
		}
	}

	fingerprint(v.AppliedVersionID)
	fingerprint(in.Deployed[v.Name])

	unmatched := servedByApplied(v, *in)
	if len(unmatched) == 0 {
		return
	}

	applied, ok := in.created(v.AppliedVersionID)
	if !ok {
		return
	}

	older := make([]credhub.CredentialVersion, 0, len(list))

	for _, cv := range list {
		if !cv.CreatedAt.IsZero() && cv.CreatedAt.Before(applied) {
			older = append(older, cv)
		}
	}

	sort.SliceStable(older, func(i, j int) bool { return older[i].CreatedAt.After(older[j].CreatedAt) })

	for _, cv := range older {
		for fp := range unmatched {
			if fp == in.VersionFingerprint[cv.ID] {
				delete(unmatched, fp)
			}
		}

		if len(unmatched) == 0 {
			return
		}

		fingerprint(cv.ID)

		if fp := in.VersionFingerprint[cv.ID]; fp != "" {
			delete(unmatched, fp)
		}
	}
}

// servedByApplied returns the fingerprints that the entry's applied VMs
// served in the scan and that no version read so far names.
func servedByApplied(v DivergentVariable, in ReconcileInput) map[string]bool {
	known := map[string]bool{}
	for _, fp := range in.VersionFingerprint {
		known[fp] = true
	}

	out := map[string]bool{}

	for _, l := range in.Expected[v.Name] {
		o, seen := in.Observed[l.Locator]
		if !seen || !in.fresh(o) || known[o.Fingerprint] {
			continue
		}

		if vm, ok := v.VMs[o.VMCID]; ok && vm.State == DivergentVMApplied {
			out[o.Fingerprint] = true
		}
	}

	return out
}

// observedLeaves maps each listener a scan's probes reached to the leaf it
// served and the VM whose address the probe dialled.
func observedLeaves(scan InstanceScan) map[string]ObservedLeaf {
	byIP := map[string]string{}

	for _, vm := range scan.VMs {
		for _, ip := range vm.IPs {
			if ip != "" && vm.CID != "" {
				byIP[ip] = vm.CID
			}
		}
	}

	out := map[string]ObservedLeaf{}

	for _, r := range scan.Material.Raw {
		if r.Source != SourceProbe || r.State != StateOK || r.Fingerprint == "" {
			continue
		}

		key, ip, ok := listenerKey(r.Locator)
		if !ok {
			continue
		}

		out[key] = ObservedLeaf{VMCID: byIP[ip], Fingerprint: r.Fingerprint, At: r.ObservedAt}
	}

	return out
}

// listenerKey turns a probe locator, "<group>/<index> <ip>:<port>", into
// the locator of the expected listener, "<group>/<index>:<port>", and the
// IP the probe dialled.
func listenerKey(locator string) (string, string, bool) {
	name, addr, ok := strings.Cut(locator, " ")
	if !ok || name == "" {
		return "", "", false
	}

	ip, port, err := net.SplitHostPort(addr)
	if err != nil || ip == "" || port == "" {
		return "", "", false
	}

	return name + ":" + port, ip, true
}

// locatorIP returns the IP a probe locator names, or an empty string.
func locatorIP(locator string) string {
	_, ip, ok := listenerKey(locator)
	if !ok {
		return ""
	}

	return ip
}

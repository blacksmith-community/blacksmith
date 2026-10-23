package upgrade

import (
	"maps"
	"slices"
)

// cloneEach returns a copy of the slice with each element cloned, and nil
// for nil.
func cloneEach[T any](items []T, clone func(T) T) []T {
	if items == nil {
		return nil
	}

	copied := make([]T, len(items))
	for i, item := range items {
		copied[i] = clone(item)
	}

	return copied
}

// Clone returns a copy of the spec, or nil for nil.
func (s *CertificateTaskSpec) Clone() *CertificateTaskSpec {
	if s == nil {
		return nil
	}

	copied := *s

	return &copied
}

// Clone returns a copy of the rotation that shares nothing with it, or nil
// for nil.
func (r *InstanceRotation) Clone() *InstanceRotation {
	if r == nil {
		return nil
	}

	copied := *r
	copied.Approved = r.Approved.Clone()
	copied.Variables = cloneEach(r.Variables, VariableRotation.Clone)
	copied.VMs = cloneEach(r.VMs, VMJournal.Clone)
	copied.Converge = r.Converge.Clone()
	copied.Deploy = r.Deploy.Clone()
	copied.Pending = r.Pending.Clone()

	if r.Decision != nil {
		decision := *r.Decision
		copied.Decision = &decision
	}

	return &copied
}

// Clone returns a copy of the effects that shares nothing with them.
func (a ApprovedEffects) Clone() ApprovedEffects {
	a.Selected = slices.Clone(a.Selected)
	a.Additional = slices.Clone(a.Additional)
	a.VMCIDs = slices.Clone(a.VMCIDs)

	return a
}

// Clone returns a copy of the variable's record that shares nothing with
// it.
func (v VariableRotation) Clone() VariableRotation {
	v.Baseline = slices.Clone(v.Baseline)
	v.NewNotAfter = copyTime(v.NewNotAfter)
	v.Validation = slices.Clone(v.Validation)

	return v
}

// Clone returns a copy of the decision that shares nothing with it, or nil
// for nil.
func (d *PendingDecision) Clone() *PendingDecision {
	if d == nil {
		return nil
	}

	copied := *d
	copied.Candidates = cloneEach(d.Candidates, Candidate.Clone)
	copied.Changes = slices.Clone(d.Changes)

	return &copied
}

// Clone returns a copy of the candidate that shares nothing with it.
func (c Candidate) Clone() Candidate {
	c.Checks = slices.Clone(c.Checks)

	return c
}

// Clone returns a copy of the converge deploy, or nil for nil.
func (c *ConvergeDeploy) Clone() *ConvergeDeploy {
	if c == nil {
		return nil
	}

	copied := *c

	return &copied
}

// Clone returns a copy of the deploy's outcome.
func (o *DeployOutcome) Clone() *DeployOutcome {
	if o == nil {
		return nil
	}

	copied := *o

	return &copied
}

// Clone returns a copy of the VM's journal that shares nothing with it.
func (j VMJournal) Clone() VMJournal {
	j.JobDirs = maps.Clone(j.JobDirs)
	j.Files = cloneEach(j.Files, FileEntry.Clone)
	j.Processes = slices.Clone(j.Processes)
	j.Targets = slices.Clone(j.Targets)

	return j
}

// Clone returns a copy of the file entry. It holds no slices, maps, or
// pointers, and has a Clone so a field added later is copied in one place.
func (f FileEntry) Clone() FileEntry {
	return f
}

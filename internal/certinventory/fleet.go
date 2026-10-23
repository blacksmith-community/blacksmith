package certinventory

import (
	"crypto/x509"
	"encoding/pem"
	"slices"
	"sort"
	"time"
)

// Fleet is every logical certificate the broker knows, grouped across all
// the scans it was built from. A Fleet never changes after Aggregate returns
// it, so any number of readers may use it at once.
type Fleet struct {
	certs []Certificate
	reps  map[string]*x509.Certificate
	index map[string]int
	// everScanned says at least one scan in the input had succeeded at some
	// point, so an empty fleet means nothing was found and not that nothing
	// was ever read.
	everScanned bool
}

// ItemDetail is one logical certificate with its public certificate and the
// facts parsed from it. The PEM holds the public certificate only.
type ItemDetail struct {
	Certificate
	PEM   string    `json:"pem"`
	Facts CertFacts `json:"facts"`
}

// Certificates returns the fleet's certificates sorted by ID. Callers must
// not change what they get back.
func (f *Fleet) Certificates() []Certificate {
	if f == nil {
		return nil
	}

	return f.certs
}

// Item returns one certificate by ID with its public PEM and parsed facts.
func (f *Fleet) Item(id string) (ItemDetail, bool) {
	if f == nil {
		return ItemDetail{}, false
	}

	i, ok := f.index[id]
	if !ok {
		return ItemDetail{}, false
	}

	detail := ItemDetail{Certificate: f.certs[i]}

	if rep := f.reps[id]; rep != nil {
		detail.PEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rep.Raw}))
		detail.Facts = Facts(rep)
	}

	return detail, true
}

// credEntry is one CredHub certificate and the scan that read it.
type credEntry struct {
	ch   CredHubCert
	scan *InstanceScan
}

// Aggregate groups the material of every scan into one set of logical
// certificates. A CredHub name seen in several scans becomes one
// certificate that keeps the versions from the newest scan and lists every
// owner. All raw observations go through Group together, so a certificate
// shared by many instances is one certificate. Findings, coverage, expiry,
// health, attention, and rotatability are applied last. div maps an instance
// ID to its divergence record, and each certificate a record names gets its
// Divergence field from it before the findings. A nil div means there are
// none. The result does not depend on the order of scans.
func Aggregate(scans []InstanceScan, s Settings, caps Capabilities, div map[string]*Divergence, now time.Time) *Fleet {
	ordered := slices.Clone(scans)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Ref.ID < ordered[j].Ref.ID })

	fleet := &Fleet{}
	byInstance := map[string]*InstanceScan{}

	var (
		raw     []RawObservation
		pinned  = map[string]string{}
		hosts   = map[string][]string{}
		cas     = map[string]*x509.Certificate{}
		caScan  = map[string]*InstanceScan{}
		drift   bool
		entries = map[string][]credEntry{}
	)

	for i := range ordered {
		scan := &ordered[i]
		byInstance[scan.Ref.ID] = scan

		if !scan.LastSuccessAt.IsZero() {
			fleet.everScanned = true
		}

		for _, r := range scan.Material.Raw {
			if scan.Stale && r.State == StateOK {
				r.State = StateStale
			}

			raw = append(raw, r)
		}

		for _, ch := range scan.Material.CredHub {
			entries[ch.Name] = append(entries[ch.Name], credEntry{ch: ch, scan: scan})
		}

		for name, ca := range scan.Material.SigningCAs {
			if ca != nil && (caScan[name] == nil || newer(scan, caScan[name])) {
				cas[name], caScan[name] = ca, scan
			}
		}

		if len(scan.Material.Hosts) > 0 {
			hosts[scan.Ref.ID] = append(hosts[scan.Ref.ID], scan.Material.Hosts...)
		}

		for endpoint, msg := range scan.Material.PinnedFailures {
			if _, seen := pinned[endpoint]; !seen {
				pinned[endpoint] = msg
			}
		}

		drift = drift || scan.Material.ServicesCADrift
	}

	chosen, primary, extras := chooseCredHub(entries)

	certs, reps := Group(chosen, raw)
	fleet.certs, fleet.reps = certs, reps
	fleet.index = make(map[string]int, len(certs))

	for i := range certs {
		fleet.index[certs[i].ID] = i
	}

	fleet.stampCredHub(primary, extras)

	expected := map[string]int{}

	for i := range ordered {
		for _, el := range ordered[i].Material.Expected {
			if el.Key == "" {
				continue
			}

			id := hashID("credhub:" + el.Key)
			if _, ok := fleet.index[id]; ok {
				expected[id]++
			}
		}
	}

	diverged := fleet.applyDivergence(div, byInstance)

	ApplyFindings(fleet.certs, FindingInput{
		CAs:               cas,
		Reps:              reps,
		Hosts:             hosts,
		PinnedFailures:    pinned,
		ServicesCADrift:   drift,
		Expected:          expected,
		DivergedListeners: diverged,
		Now:               now,
	})

	for i := range fleet.certs {
		c := &fleet.certs[i]

		c.EffectiveNotAfter = EffectiveNotAfter(c)
		applyExpiry(c, s, now)
		c.Health = HealthOf(c.Findings, c.Coverage)
		c.Attention = NeedsAttention(c)

		vms, gate := owningInstance(c, byInstance)
		c.Rotation = RotatabilityOf(c, caps, caps.DirectorName, vms, gate)
	}

	return fleet
}

// scanTime is when the data in a scan was read.
func scanTime(s *InstanceScan) time.Time {
	if !s.ScannedAt.IsZero() {
		return s.ScannedAt
	}

	return s.LastSuccessAt
}

func newer(a, b *InstanceScan) bool {
	return scanTime(a).After(scanTime(b))
}

// chooseCredHub picks one entry for each CredHub name, the one from the
// newest scan, and lists the entries of other owners that read the same
// name. Ties go to the lowest instance ID, since entries arrive in ID order.
func chooseCredHub(entries map[string][]credEntry) ([]CredHubCert, map[string]credEntry, map[string][]credEntry) {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}

	sort.Strings(names)

	chosen := make([]CredHubCert, 0, len(names))
	primary := map[string]credEntry{}
	extras := map[string][]credEntry{}

	for _, name := range names {
		list := entries[name]
		best := 0

		for i := 1; i < len(list); i++ {
			if newer(list[i].scan, list[best].scan) {
				best = i
			}
		}

		chosen = append(chosen, list[best].ch)
		primary[name] = list[best]

		seen := map[Owner]bool{list[best].ch.Owner: true}

		for i, e := range list {
			if i == best || seen[e.ch.Owner] {
				continue
			}

			seen[e.ch.Owner] = true
			extras[name] = append(extras[name], e)
		}
	}

	return chosen, primary, extras
}

// stampCredHub gives each CredHub observation the time its scan read it,
// marks the ones from a stale scan, and adds the owners that read the same
// name in other scans. Group leaves the time at zero.
func (f *Fleet) stampCredHub(primary map[string]credEntry, extras map[string][]credEntry) {
	for name, p := range primary {
		i, ok := f.index[hashID("credhub:"+name)]
		if !ok {
			continue
		}

		c := &f.certs[i]

		for j := range c.Observations {
			o := &c.Observations[j]
			if o.Source != SourceCredHub || o.Locator != name || o.Owner != p.ch.Owner || !o.ObservedAt.IsZero() {
				continue
			}

			stampObservation(o, p.scan)

			break
		}

		if len(extras[name]) == 0 {
			continue
		}

		obs := slices.Clone(c.Observations)

		for _, e := range extras[name] {
			ch := e.ch
			o := credhubObservation(&ch)
			stampObservation(&o, e.scan)
			obs = append(obs, o)
		}

		sort.SliceStable(obs, func(a, b int) bool { return obsLess(obs[a], obs[b]) })
		c.Observations = obs
		c.Owners, c.AffectedInstances = ownersOf(obs)
	}
}

func stampObservation(o *Observation, scan *InstanceScan) {
	o.ObservedAt = scanTime(scan)

	if scan.Stale && o.State == StateOK {
		o.State = StateStale
	}
}

// ownersOf lists the distinct owners of observations in order, and counts
// the distinct instances among them.
func ownersOf(obs []Observation) ([]Owner, int) {
	seen := map[Owner]bool{}
	instances := map[string]bool{}

	var owners []Owner

	for _, o := range obs {
		if !seen[o.Owner] {
			seen[o.Owner] = true
			owners = append(owners, o.Owner)
		}

		if o.Owner.InstanceID != "" {
			instances[o.Owner.InstanceID] = true
		}
	}

	sort.Slice(owners, func(a, b int) bool { return ownerLess(owners[a], owners[b]) })

	return owners, len(instances)
}

// applyExpiry sets the expiry status, days left, and the reason an expiry
// is unknown, from the certificate's effective expiry.
func applyExpiry(c *Certificate, s Settings, now time.Time) {
	c.Expiry, c.DaysLeft = ExpiryOf(c.EffectiveNotAfter, now, s.WarningDays, s.CriticalDays)
	c.ExpiryReason = ""

	if c.Expiry != ExpiryUnknown {
		return
	}

	c.ExpiryReason = "No observation of this certificate gave an expiry date."

	for _, o := range c.Observations {
		if o.State == StateFailed && o.Error != "" {
			c.ExpiryReason = o.Error

			break
		}
	}
}

// owningInstance returns the VM count and health gate of the instance that
// owns the certificate's CredHub variable, or of its first instance owner.
func owningInstance(c *Certificate, scans map[string]*InstanceScan) (int, string) {
	id := ""

	for _, o := range c.Observations {
		if o.Source == SourceCredHub && o.Owner.InstanceID != "" {
			id = o.Owner.InstanceID

			break
		}
	}

	if id == "" {
		for _, o := range c.Owners {
			if o.InstanceID != "" {
				id = o.InstanceID

				break
			}
		}
	}

	scan := scans[id]
	if scan == nil {
		return 0, ""
	}

	return len(scan.VMs), scan.HealthGate
}

// applyDivergence sets the Divergence field of each certificate whose
// CredHub name has an entry in the record of one of its owning instances.
// When several owners have one, a reverted entry wins. It returns the
// probed listeners that the entries explain, keyed as
// FindingInput.DivergedListeners describes: each probe observation of the
// certificate that serves the entry's applied version from an IP of a VM
// that the entry lists.
func (f *Fleet) applyDivergence(div map[string]*Divergence, scans map[string]*InstanceScan) map[string]bool {
	if len(div) == 0 {
		return nil
	}

	diverged := map[string]bool{}

	for i := range f.certs {
		c := &f.certs[i]
		if c.Key == "" {
			continue
		}

		for _, owner := range c.Owners {
			rec := div[owner.InstanceID]
			if owner.InstanceID == "" || rec == nil {
				continue
			}

			ref := DivergenceRefOf(rec, c.Key)
			if ref == nil {
				continue
			}

			if c.Divergence == nil || (ref.State == DivergenceReverted && c.Divergence.State != DivergenceReverted) {
				c.Divergence = ref
			}

			markDivergedListeners(diverged, c, rec.entry(c.Key), scans[owner.InstanceID])
		}
	}

	return diverged
}

// markDivergedListeners adds each probe observation of c that serves the
// entry's applied version and whose locator names an IP of a VM the entry
// lists. A listed VM serving any other certificate is still a mismatch,
// and an entry with no applied fingerprint explains no listener.
func markDivergedListeners(diverged map[string]bool, c *Certificate, e *DivergentVariable, scan *InstanceScan) {
	if e == nil || scan == nil || e.AppliedFingerprint == "" {
		return
	}

	ips := map[string]bool{}

	for _, vm := range scan.VMs {
		if _, listed := e.VMs[vm.CID]; !listed || vm.CID == "" {
			continue
		}

		for _, ip := range vm.IPs {
			if ip != "" {
				ips[ip] = true
			}
		}
	}

	for _, o := range c.Observations {
		if o.Source == SourceProbe && o.Fingerprint == e.AppliedFingerprint && ips[locatorIP(o.Locator)] {
			diverged[c.ID+"|"+o.Locator] = true
		}
	}
}

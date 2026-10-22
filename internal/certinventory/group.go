package certinventory

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"sort"
	"strings"
)

// Values for Observation.Matches and Observation.State.
const (
	MatchDeployed = "deployed"
	MatchLatest   = "latest"
	MatchOther    = "other"

	StateOK     = "ok"
	StateStale  = "stale"
	StateFailed = "failed"
)

// CredHubCert is one certificate variable read from CredHub.
type CredHubCert struct {
	Name     string
	Owner    Owner
	Versions Versions
	Deployed *x509.Certificate
	Latest   *x509.Certificate
	Chain    []*x509.Certificate
	// Err says why the certificate could not be read. A nil Deployed with a
	// non-empty Err means the expiry is unknown. It is a string so that a
	// cached scan marshals the message instead of an empty object.
	Err string
}

// RawObservation is an observation before grouping, with the parsed
// certificate it came from and the listener it was probed on, when it has
// them. A failed probe has no Cert.
type RawObservation struct {
	Observation
	Cert     *x509.Certificate
	Listener *Listener
}

// Group turns CredHub certificates and raw observations into logical
// certificates, in the order spec 7.3 gives. It returns the certificates
// sorted by ID, and a map from each certificate ID to its representative
// public certificate. The representative is the CredHub deployed version
// when there is one, then the first observation in ID order that carries a
// certificate, and last the CredHub latest version.
//
// Group leaves Expiry, DaysLeft, Health, Attention, Findings, Coverage, and
// Rotation for later steps. It must run once over the whole fleet's
// material, never inside one scan.
func Group(credhub []CredHubCert, raw []RawObservation) ([]Certificate, map[string]*x509.Certificate) {
	groups := map[string]*group{}

	var credhubOrder []*group

	// Sorted names make the owner of a shared fingerprint independent of
	// input order: the first name wins.
	sorted := make([]*CredHubCert, 0, len(credhub))
	for i := range credhub {
		sorted = append(sorted, &credhub[i])
	}

	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, ch := range sorted {
		id := hashID("credhub:" + ch.Name)

		if _, dup := groups[id]; dup {
			continue
		}

		g := &group{id: id, key: ch.Name, credhub: ch}
		groups[id] = g
		credhubOrder = append(credhubOrder, g)

		g.add(credhubObservation(ch), nil)
	}

	byFP := map[string]*group{} // deployed or latest fingerprint -> CredHub group

	// Deployed fingerprints win over latest ones when both name a group.
	for _, g := range credhubOrder {
		if g.credhub.Deployed != nil {
			claim(byFP, Fingerprint(g.credhub.Deployed), g)
		}
	}

	for _, g := range credhubOrder {
		if g.credhub.Latest != nil {
			claim(byFP, Fingerprint(g.credhub.Latest), g)
		}
	}

	for i := range raw {
		r := raw[i]
		obs := r.Observation

		if r.Cert != nil {
			if obs.Fingerprint == "" {
				obs.Fingerprint = Fingerprint(r.Cert)
			}

			if obs.Serial == "" {
				obs.Serial = r.Cert.SerialNumber.Text(16)
			}

			if obs.NotAfter == nil {
				t := r.Cert.NotAfter
				obs.NotAfter = &t
			}
		}

		if g, ok := byFP[obs.Fingerprint]; ok && obs.Fingerprint != "" {
			obs.Matches = matchKind(g.credhub, obs.Fingerprint)
			g.add(obs, r.Cert)

			continue
		}

		if g := listenerGroup(credhubOrder, r.Listener, obs.Owner); g != nil {
			if obs.Fingerprint != "" {
				obs.Matches = MatchOther
			}

			g.add(obs, r.Cert)

			continue
		}

		id, key := hashID("fp:"+obs.Fingerprint), "sha256:"+obs.Fingerprint
		if obs.Fingerprint == "" {
			// Nothing identifies it, but dropping it would hide a failure
			// and let coverage read complete, so it gets a group of its own.
			id, key = hashID("probe:"+obs.Owner.InstanceID+"\x00"+obs.Locator), obs.Locator
		}

		g, ok := groups[id]
		if !ok {
			g = &group{id: id, key: key}
			groups[id] = g
		}

		g.add(obs, r.Cert)
	}

	certs := make([]Certificate, 0, len(groups))
	reps := map[string]*x509.Certificate{}

	for _, g := range groups {
		c, rep := g.build()

		certs = append(certs, c)

		if rep != nil {
			reps[c.ID] = rep
		}
	}

	sort.Slice(certs, func(i, j int) bool { return certs[i].ID < certs[j].ID })

	return certs, reps
}

type group struct {
	id      string
	key     string
	credhub *CredHubCert
	obs     []Observation
	certs   []*x509.Certificate // parallel to obs; nil when the observation has none
}

func (g *group) add(o Observation, c *x509.Certificate) {
	g.obs = append(g.obs, o)
	g.certs = append(g.certs, c)
}

func (g *group) build() (Certificate, *x509.Certificate) {
	idx := make([]int, len(g.obs))
	for i := range idx {
		idx[i] = i
	}

	sort.SliceStable(idx, func(a, b int) bool { return obsLess(g.obs[idx[a]], g.obs[idx[b]]) })

	c := Certificate{ID: g.id, Key: g.key, Observations: make([]Observation, 0, len(idx))}

	var rep *x509.Certificate

	if g.credhub != nil {
		v := g.credhub.Versions
		c.Versions = &v
		c.Variable = lastSegment(g.credhub.Name)

		rep = g.credhub.Deployed
	}

	seenOwner := map[Owner]bool{}
	instances := map[string]bool{}

	for _, i := range idx {
		c.Observations = append(c.Observations, g.obs[i])

		if rep == nil && g.certs[i] != nil {
			rep = g.certs[i]
		}

		o := g.obs[i].Owner
		if !seenOwner[o] {
			seenOwner[o] = true
			c.Owners = append(c.Owners, o)
		}

		if o.InstanceID != "" {
			instances[o.InstanceID] = true
		}
	}

	sort.Slice(c.Owners, func(a, b int) bool { return ownerLess(c.Owners[a], c.Owners[b]) })

	c.AffectedInstances = len(instances)

	if rep == nil && g.credhub != nil {
		rep = g.credhub.Latest
	}

	if rep != nil {
		f := Facts(rep)
		c.Kind = KindOf(rep)
		c.CommonName = f.CommonName
		c.Issuer = f.Issuer
		c.SubjectKeyID = f.SubjectKeyID
		c.AuthorityKeyID = f.AuthorityKeyID
		c.DNSNames = f.DNSNames
		c.IPAddresses = f.IPAddresses
		c.ExtKeyUsages = f.ExtKeyUsages
		c.KeyAlgorithm = f.KeyAlgorithm
		c.KeyBits = f.KeyBits
	}

	return c, rep
}

func claim(m map[string]*group, fp string, g *group) {
	if _, taken := m[fp]; !taken {
		m[fp] = g
	}
}

func credhubObservation(ch *CredHubCert) Observation {
	o := Observation{
		Source:  SourceCredHub,
		Owner:   ch.Owner,
		Locator: ch.Name,
		State:   StateOK,
		Matches: MatchDeployed,
	}

	if ch.Deployed != nil {
		t := ch.Deployed.NotAfter
		o.Fingerprint = Fingerprint(ch.Deployed)
		o.Serial = ch.Deployed.SerialNumber.Text(16)
		o.NotAfter = &t
	}

	if ch.Err != "" {
		o.State = StateFailed
		o.Error = ch.Err
	}

	return o
}

func matchKind(ch *CredHubCert, fp string) string {
	if ch.Deployed != nil && Fingerprint(ch.Deployed) == fp {
		return MatchDeployed
	}

	return MatchLatest
}

// listenerGroup finds the CredHub certificate a listener's variable names
// among the certificates of the same deployment. It returns nil when the
// variable is empty or does not resolve to exactly one certificate.
func listenerGroup(all []*group, l *Listener, owner Owner) *group {
	if l == nil || l.Variable == "" {
		return nil
	}

	var found *group

	for _, g := range all {
		if lastSegment(g.credhub.Name) != l.Variable {
			continue
		}

		if owner.InstanceID != "" && g.credhub.Owner.InstanceID != "" && owner.InstanceID != g.credhub.Owner.InstanceID {
			continue
		}

		if owner.Deployment != "" && g.credhub.Owner.Deployment != "" && owner.Deployment != g.credhub.Owner.Deployment {
			continue
		}

		if found != nil {
			return nil
		}

		found = g
	}

	return found
}

func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])[:16]
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}

	return name
}

func obsLess(a, b Observation) bool {
	switch {
	case a.Source != b.Source:
		return a.Source < b.Source
	case a.Locator != b.Locator:
		return a.Locator < b.Locator
	case a.Owner != b.Owner:
		return ownerLess(a.Owner, b.Owner)
	case a.Fingerprint != b.Fingerprint:
		return a.Fingerprint < b.Fingerprint
	}

	return a.State < b.State
}

func ownerLess(a, b Owner) bool {
	switch {
	case a.Kind != b.Kind:
		return a.Kind < b.Kind
	case a.InstanceID != b.InstanceID:
		return a.InstanceID < b.InstanceID
	case a.InstanceName != b.InstanceName:
		return a.InstanceName < b.InstanceName
	case a.Deployment != b.Deployment:
		return a.Deployment < b.Deployment
	case a.ServiceName != b.ServiceName:
		return a.ServiceName < b.ServiceName
	}

	return a.PlanName < b.PlanName
}

package certinventory

import (
	"testing"
	"time"
)

func tp(t time.Time) *time.Time { return &t }

func TestExpiryOfBoundaries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	rows := []struct {
		name     string
		notAfter *time.Time
		status   ExpiryStatus
		days     *int
	}{
		{"at now", tp(now), ExpiryExpired, new(int)},
		{"two days ago", tp(now.Add(-48 * time.Hour)), ExpiryExpired, intp(-2)},
		{"partial day ago", tp(now.Add(-time.Hour)), ExpiryExpired, intp(-1)},
		{"6d23h", tp(now.Add(6*day + 23*time.Hour)), ExpiryCritical, intp(6)},
		{"7d", tp(now.Add(7 * day)), ExpiryWarning, intp(7)},
		{"29d23h", tp(now.Add(29*day + 23*time.Hour)), ExpiryWarning, intp(29)},
		{"30d", tp(now.Add(30 * day)), ExpiryOK, intp(30)},
		{"nil", nil, ExpiryUnknown, nil},
	}

	for _, row := range rows {
		status, days := ExpiryOf(row.notAfter, now, 30, 7)
		if status != row.status {
			t.Errorf("%s: status = %s, want %s", row.name, status, row.status)
		}

		switch {
		case row.days == nil && days != nil:
			t.Errorf("%s: days = %d, want nil", row.name, *days)
		case row.days != nil && (days == nil || *days != *row.days):
			t.Errorf("%s: days = %v, want %d", row.name, days, *row.days)
		}
	}
}

func intp(i int) *int { return &i }

func TestHealthOfOrder(t *testing.T) {
	t.Parallel()

	complete := Coverage{Complete: true}
	incomplete := Coverage{Complete: false}
	failed := Coverage{Complete: true, SourceStates: map[Source]string{SourceProbe: "failed"}}
	errF := Finding{Code: "chain_broken", Severity: SeverityError}
	warnF := Finding{Code: "weak_key", Severity: SeverityWarning}
	infoF := Finding{Code: "deploy_pending", Severity: SeverityInfo}

	rows := []struct {
		name string
		f    []Finding
		cov  Coverage
		want Health
	}{
		{"error", []Finding{warnF, errF}, incomplete, HealthError},
		{"warning with incomplete", []Finding{warnF}, incomplete, HealthWarning},
		{"info with incomplete", []Finding{infoF}, incomplete, HealthUnknown},
		{"failed source", nil, failed, HealthUnknown},
		{"nothing", nil, complete, HealthOK},
	}

	for _, row := range rows {
		if got := HealthOf(row.f, row.cov); got != row.want {
			t.Errorf("%s: got %s, want %s", row.name, got, row.want)
		}
	}
}

func TestNeedsAttention(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name string
		c    Certificate
		want bool
	}{
		{"expired", Certificate{Expiry: ExpiryExpired, Health: HealthOK}, true},
		{"critical", Certificate{Expiry: ExpiryCritical, Health: HealthOK}, true},
		{"warning health error", Certificate{Expiry: ExpiryWarning, Health: HealthError}, true},
		{"unconverged", Certificate{Expiry: ExpiryOK, Health: HealthOK, Divergence: &DivergenceRef{State: "unconverged"}}, true},
		{"reverted", Certificate{Expiry: ExpiryOK, Health: HealthOK, Divergence: &DivergenceRef{State: "reverted"}}, true},
		{"quiet warning", Certificate{Expiry: ExpiryWarning, Health: HealthOK}, false},
	}

	for _, row := range rows {
		if got := NeedsAttention(&row.c); got != row.want {
			t.Errorf("%s: got %v, want %v", row.name, got, row.want)
		}
	}
}

func TestEffectiveNotAfterNeverImprovesOnPartialCoverage(t *testing.T) {
	t.Parallel()

	deployed := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	later := time.Date(2027, 10, 21, 0, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)

	probe := func(n time.Time) Observation { return Observation{Source: SourceProbe, NotAfter: tp(n)} }
	build := func(complete bool, obs ...Observation) *Certificate {
		return &Certificate{
			Versions:     &Versions{DeployedNotAfter: tp(deployed)},
			Observations: obs,
			Coverage:     Coverage{ExpectedListeners: 3, Complete: complete},
		}
	}

	rows := []struct {
		name string
		c    *Certificate
		want time.Time
	}{
		{"partial", build(false, probe(later), probe(later)), deployed},
		{"complete and agreed", build(true, probe(later), probe(later), probe(later)), later},
		{"one earlier", build(true, probe(later), probe(later), probe(earlier)), earlier},
		{"one earlier partial", build(false, probe(later), probe(earlier)), earlier},
		{"no probes", build(true), deployed},
	}

	for _, row := range rows {
		got := EffectiveNotAfter(row.c)
		if got == nil || !got.Equal(row.want) {
			t.Errorf("%s: got %v, want %v", row.name, got, row.want)
		}
	}
}

func TestEffectiveNotAfterOtherSources(t *testing.T) {
	t.Parallel()

	a := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	b := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	c := &Certificate{Observations: []Observation{{NotAfter: tp(a)}, {}, {NotAfter: tp(b)}}}

	if got := EffectiveNotAfter(c); got == nil || !got.Equal(b) {
		t.Errorf("got %v, want %v", got, b)
	}

	if got := EffectiveNotAfter(&Certificate{}); got != nil {
		t.Errorf("got %v, want nil", got)
	}

	// CredHub's undeployed latest version gives no expiry.
	pending := &Certificate{Observations: []Observation{
		{Source: SourceCredHub, Matches: MatchLatest, NotAfter: tp(b)},
		{Source: SourceProbe, Matches: MatchOther, NotAfter: tp(a)},
	}}

	if got := EffectiveNotAfter(pending); got == nil || !got.Equal(a) {
		t.Errorf("got %v, want %v", got, a)
	}
}

func TestSummarizeCounts(t *testing.T) {
	t.Parallel()

	ok := Coverage{Complete: true}
	owner := func(id string) []Owner { return []Owner{{Kind: OwnerInstance, InstanceID: id}} }
	certs := []Certificate{
		{Expiry: ExpiryExpired, Health: HealthOK, Coverage: ok, Owners: owner("i1")},
		{Expiry: ExpiryCritical, Health: HealthError, Coverage: ok, Owners: owner("i1")},
		{Expiry: ExpiryWarning, Health: HealthOK, Coverage: ok, Owners: owner("i2"),
			Divergence: &DivergenceRef{State: "unconverged"}},
		{Expiry: ExpiryOK, Health: HealthOK, Coverage: Coverage{Complete: true, Stale: true}},
		{Expiry: ExpiryUnknown, Health: HealthUnknown, Coverage: Coverage{Complete: false}, Owners: owner("i3")},
		{Expiry: ExpiryOK, Health: HealthOK, Coverage: ok, Owners: owner("i4"),
			Divergence: &DivergenceRef{State: "reverted"}},
	}

	s := Summarize(certs)
	want := Summary{
		Certificates: 6, AffectedInstances: 4, Expired: 1, Critical: 1, Warning: 1,
		ExpiryOK: 2, ExpiryUnknown: 1, HealthErrors: 1, NeedsAttention: 4,
		Unconverged: 1, Reverted: 1, Stale: 1, IncompleteCoverage: 1,
	}

	if s != want {
		t.Errorf("got %+v, want %+v", s, want)
	}
}

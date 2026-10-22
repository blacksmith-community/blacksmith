package certinventory

import (
	"reflect"
	"strings"
	"testing"

	"blacksmith/internal/credhub"
)

func allOn() Capabilities {
	return Capabilities{
		CredHub:         credhub.StateConfigured,
		DirectorVersion: "282.0.0",
		ForceLatest:     true,
		RotationEnabled: true,
		PatchEnabled:    true,
		ProbeEnabled:    true,
	}
}

func rotLeaf() *Certificate {
	return &Certificate{
		Key:      "/bosh-lab/redis-i1/redis_crt",
		Kind:     KindLeaf,
		Versions: &Versions{},
		Owners:   []Owner{{Kind: OwnerInstance, InstanceID: "i1", Deployment: "redis-i1"}},
	}
}

func TestRotatabilityOf(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name        string
		cert        *Certificate
		caps        Capabilities
		vms         int
		gate        string
		allowed     bool
		modes       []string
		wantReasons []string
	}{
		{name: "everything on, one VM", cert: rotLeaf(), caps: allOn(), vms: 1, allowed: true, modes: []string{"redeploy", "patch"}},
		{name: "patch off", cert: rotLeaf(), caps: func() Capabilities { c := allOn(); c.PatchEnabled = false; return c }(), vms: 1,
			allowed: true, modes: []string{"redeploy"}, wantReasons: []string{"Patch in place is off on this broker."}},
		{name: "three VMs, no gate", cert: rotLeaf(), caps: allOn(), vms: 3, allowed: true, modes: []string{"redeploy"},
			wantReasons: []string{"No health gate exists for this service, so patch in place is unavailable on a multi-VM instance."}},
		{name: "three VMs with a gate", cert: rotLeaf(), caps: allOn(), vms: 3, gate: "redis-cluster", allowed: true, modes: []string{"redeploy", "patch"}},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()

			got := RotatabilityOf(r.cert, r.caps, "bosh-lab", r.vms, r.gate)
			if got.Allowed != r.allowed || !reflect.DeepEqual(got.Modes, r.modes) || !reflect.DeepEqual(got.Reasons, r.wantReasons) {
				t.Errorf("got %+v, want allowed %v modes %v reasons %v", got, r.allowed, r.modes, r.wantReasons)
			}
		})
	}

	blocked := []struct {
		name   string
		cert   *Certificate
		caps   Capabilities
		reason string
	}{
		{"a CA", func() *Certificate { c := rotLeaf(); c.Kind = KindCA; return c }(), allOn(), "leaf"},
		{"outside the namespace", func() *Certificate { c := rotLeaf(); c.Key = "/bosh-lab/other-dep/redis_crt"; return c }(), allOn(), "not under /bosh-lab/"},
		{"another director", func() *Certificate { c := rotLeaf(); c.Key = "/elsewhere/redis-i1/redis_crt"; return c }(), allOn(), "not under /bosh-lab/"},
		{"not a CredHub variable", func() *Certificate { c := rotLeaf(); c.Versions = nil; return c }(), allOn(), "not a CredHub variable"},
		{"signing CA changed", func() *Certificate {
			c := rotLeaf()
			c.Findings = []Finding{{Code: FindingSigningCAChanged, Severity: SeverityError}}

			return c
		}(), allOn(), "CA that would sign"},
		{"director 278", rotLeaf(), Capabilities{
			CredHub: credhub.StateConfigured, DirectorVersion: "278.0.0", ForceLatest: false,
			ForceLatestReason: "The director is older than 279.", RotationEnabled: true, PatchEnabled: true,
		}, "The director is older than 279."},
		{"CredHub not configured", rotLeaf(), func() Capabilities { c := allOn(); c.CredHub = credhub.StateNotConfigured; return c }(), "CredHub not configured"},
		{"CredHub refused", rotLeaf(), func() Capabilities {
			c := allOn()
			c.CredHub, c.CredHubReason = credhub.StateRefused, "403 from UAA"

			return c
		}(), "CredHub unavailable: 403 from UAA"},
		{"rotation off", rotLeaf(), func() Capabilities { c := allOn(); c.RotationEnabled = false; return c }(), "Rotation is off on this broker (certificates.rotation.enabled)."},
	}

	for _, r := range blocked {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()

			got := RotatabilityOf(r.cert, r.caps, "bosh-lab", 1, "")
			if got.Allowed || len(got.Modes) != 0 {
				t.Fatalf("got %+v, want not allowed with no modes", got)
			}

			if !strings.Contains(strings.Join(got.Reasons, "\n"), r.reason) {
				t.Errorf("reasons %q lack %q", got.Reasons, r.reason)
			}
		})
	}
}

func TestRotatabilityNamesEveryFailingCondition(t *testing.T) {
	t.Parallel()

	c := rotLeaf()
	c.Kind = KindCA

	got := RotatabilityOf(c, Capabilities{CredHub: credhub.StateNotConfigured}, "bosh-lab", 1, "")
	if got.Allowed || len(got.Reasons) != 4 {
		t.Errorf("got %+v, want 4 reasons (CredHub, CA, director gate, switch)", got)
	}
}

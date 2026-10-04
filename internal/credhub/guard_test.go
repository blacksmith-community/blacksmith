package credhub_test

import (
	"errors"
	"slices"
	"testing"

	"blacksmith/internal/credhub"
)

const (
	labDirector   = "ocfp-cf1-lab-ocf-bosh"
	labBroker     = "ocfp-cf1-lab-ocf-blacksmith"
	labGUID       = "65cc15f9-53da-40b2-a204-ed915e6091f7"
	otherGUID     = "b5a35ed8-178d-4737-80e1-661770605d39"
	labDeployment = "valkey-standalone-" + labGUID
	labPrefix     = "/" + labDirector + "/" + labDeployment + "/"
)

func labPolicy() credhub.Policy {
	return credhub.Policy{
		DirectorName:         labDirector,
		InfoDirectorName:     labDirector,
		PlanIDs:              []string{"valkey-standalone", "valkey-cluster"},
		ProtectedDeployments: []string{labBroker},
	}
}

type deploymentPrefixCase struct {
	name    string
	target  credhub.Target
	mutate  func(*credhub.Policy)
	want    string
	wantErr error
}

//nolint:funlen // one table of guard cases reads better than several
func deploymentPrefixCases() []deploymentPrefixCase {
	return []deploymentPrefixCase{
		{
			name:   "standalone deployment for its own instance",
			target: credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			want:   labPrefix,
		},
		{
			name:   "cluster deployment for its own instance",
			target: credhub.Target{InstanceID: labGUID, DeploymentName: "valkey-cluster-" + labGUID},
			want:   "/" + labDirector + "/valkey-cluster-" + labGUID + "/",
		},
		{
			name:    "info name differs from the configured name",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.InfoDirectorName = otherDirector },
			wantErr: credhub.ErrDirectorNameMismatch,
		},
		{
			name:    "info name empty",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.InfoDirectorName = "" },
			wantErr: credhub.ErrDirectorNameMismatch,
		},
		{
			name:    "configured director name empty",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.DirectorName = ""; p.InfoDirectorName = "" },
			wantErr: credhub.ErrDirectorNameInvalid,
		},
		{
			name:    "configured director name with a slash",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.DirectorName = "lab/bosh"; p.InfoDirectorName = "lab/bosh" },
			wantErr: credhub.ErrDirectorNameInvalid,
		},
		{
			name:    "configured director name with a star",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.DirectorName = "*"; p.InfoDirectorName = "*" },
			wantErr: credhub.ErrDirectorNameInvalid,
		},
		{
			name:    "configured director name with a percent",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.DirectorName = "lab%"; p.InfoDirectorName = "lab%" },
			wantErr: credhub.ErrDirectorNameInvalid,
		},
		{
			name:    "instance id that is not a GUID",
			target:  credhub.Target{InstanceID: "lastop-1696000000", DeploymentName: "valkey-standalone-lastop-1696000000"},
			wantErr: credhub.ErrInstanceIDNotGUID,
		},
		{
			name:    "instance id empty",
			target:  credhub.Target{InstanceID: "", DeploymentName: labDeployment},
			wantErr: credhub.ErrInstanceIDNotGUID,
		},
		{
			name:    "instance id GUID with trailing text",
			target:  credhub.Target{InstanceID: labGUID + "x", DeploymentName: labDeployment + "x"},
			wantErr: credhub.ErrInstanceIDNotGUID,
		},
		{
			name:    "deployment for another instance",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "valkey-standalone-" + otherGUID},
			wantErr: credhub.ErrDeploymentNotForInstance,
		},
		{
			name:    "deployment equal to the GUID with no plan part",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labGUID},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "deployment with an empty plan part",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "-" + labGUID},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "deployment name empty",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: ""},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "deployment with a path traversal",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "valkey-standalone-" + labGUID + "/../x"},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "deployment with a slash that still ends with the GUID",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "x/valkey-standalone-" + labGUID},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "deployment with a percent",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "valkey-standalone-%-" + labGUID},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "deployment with a star",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "valkey-*-" + labGUID},
			wantErr: credhub.ErrDeploymentNameInvalid,
		},
		{
			name:    "plan not in the catalog",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: "redis-standalone-" + labGUID},
			wantErr: credhub.ErrPlanNotInCatalog,
		},
		{
			name:    "empty catalog",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.PlanIDs = nil },
			wantErr: credhub.ErrPlanNotInCatalog,
		},
		{
			name:    "protected broker deployment",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labBroker},
			wantErr: credhub.ErrProtectedDeployment,
		},
		{
			name:    "protected broker deployment with a non-GUID instance",
			target:  credhub.Target{InstanceID: "lastop-1", DeploymentName: labBroker},
			wantErr: credhub.ErrProtectedDeployment,
		},
		{
			name:    "protected broker deployment with a mismatched director",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labBroker},
			mutate:  func(p *credhub.Policy) { p.InfoDirectorName = otherDirector },
			wantErr: credhub.ErrProtectedDeployment,
		},
		{
			name:    "protected deployment shaped like a service deployment",
			target:  credhub.Target{InstanceID: labGUID, DeploymentName: labDeployment},
			mutate:  func(p *credhub.Policy) { p.ProtectedDeployments = append(p.ProtectedDeployments, labDeployment) },
			wantErr: credhub.ErrProtectedDeployment,
		},
	}
}

func TestDeploymentPrefix(t *testing.T) {
	t.Parallel()

	for _, testCase := range deploymentPrefixCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			policy := labPolicy()
			if testCase.mutate != nil {
				testCase.mutate(&policy)
			}

			got, err := credhub.DeploymentPrefix(testCase.target, policy)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("DeploymentPrefix() error = %v, want %v", err, testCase.wantErr)
				}

				if got != "" {
					t.Fatalf("DeploymentPrefix() returned prefix %q alongside an error", got)
				}

				return
			}

			if err != nil {
				t.Fatalf("DeploymentPrefix() unexpected error: %v", err)
			}

			if got != testCase.want {
				t.Fatalf("DeploymentPrefix() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestFilterOwned(t *testing.T) {
	t.Parallel()

	owned := "/" + labDirector + "/" + labDeployment + "/valkey_standalone_crt"
	ownedSecond := "/" + labDirector + "/" + labDeployment + "/valkey_password"
	listed := []string{
		owned,
		"/" + labDirector + "/" + labBroker + "/blacksmith_services_ca",
		"/" + labDirector + "/" + labDeployment + "-x/valkey_standalone_crt",
		"/" + labDirector + "/" + labDeployment + "/nested/name",
		"/" + labDirector + "/" + labDeployment + "/",
		"/other-bosh/" + labDeployment + "/valkey_standalone_crt",
		"/" + labDirector + "/valkeyXstandalone-" + labGUID + "/valkey_standalone_crt",
		ownedSecond,
	}

	gotOwned, gotSkipped := credhub.FilterOwned(labPrefix, listed, labPolicy())

	if want := []string{owned, ownedSecond}; !slices.Equal(gotOwned, want) {
		t.Fatalf("owned = %q, want %q", gotOwned, want)
	}

	if want := listed[1:7]; !slices.Equal(gotSkipped, want) {
		t.Fatalf("skipped = %q, want %q", gotSkipped, want)
	}
}

func TestFilterOwnedRefusesUnderAProtectedPrefix(t *testing.T) {
	t.Parallel()

	policy := labPolicy()
	policy.ProtectedDeployments = append(policy.ProtectedDeployments, labDeployment)
	name := labPrefix + "valkey_standalone_crt"

	owned, skipped := credhub.FilterOwned(labPrefix, []string{name}, policy)
	if len(owned) != 0 || !slices.Equal(skipped, []string{name}) {
		t.Fatalf("expected a name under a protected prefix to be skipped, got owned=%q skipped=%q", owned, skipped)
	}
}

func TestFilterOwnedRefusesAMalformedPrefix(t *testing.T) {
	t.Parallel()

	name := labPrefix + "valkey_standalone_crt"

	for _, prefix := range []string{"", "/", "//", "/" + labDirector + "/", "/" + labDirector + "//", labPrefix[:len(labPrefix)-1], "/" + labDirector + "/a/b/"} {
		owned, skipped := credhub.FilterOwned(prefix, []string{name, "/x"}, labPolicy())
		if len(owned) != 0 || len(skipped) != 2 {
			t.Errorf("prefix %q: expected everything skipped, got owned=%q skipped=%q", prefix, owned, skipped)
		}
	}
}

func TestProtectedPrefixes(t *testing.T) {
	t.Parallel()

	policy := labPolicy()
	policy.ProtectedDeployments = []string{labBroker, "", labBroker, "extra"}

	got := credhub.ProtectedPrefixes(policy)
	want := []string{"/" + labDirector + "/" + labBroker + "/", "/" + labDirector + "/extra/"}

	if !slices.Equal(got, want) {
		t.Fatalf("ProtectedPrefixes() = %q, want %q", got, want)
	}
}

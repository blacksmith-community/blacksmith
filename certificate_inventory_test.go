package main

import (
	"context"
	"testing"

	"blacksmith/internal/services"
	vaultPkg "blacksmith/pkg/vault"
)

type fakeIndexSource struct {
	index   map[string]interface{}
	records map[string]map[string]interface{}
}

func (f fakeIndexSource) GetIndex(context.Context, string) (*vaultPkg.Index, error) {
	return &vaultPkg.Index{Data: f.index}, nil
}

func (f fakeIndexSource) Get(_ context.Context, path string, out interface{}) (bool, error) {
	rec, ok := f.records[path]
	if !ok {
		return false, nil
	}

	if target, ok := out.(*instanceRecord); ok {
		target.InstanceName, _ = rec["instance_name"].(string)
		target.DeploymentName, _ = rec["deployment_name"].(string)
	}

	return true, nil
}

func TestInstanceListerNamesOwnersFromVaultRecords(t *testing.T) {
	t.Parallel()

	src := fakeIndexSource{
		index: map[string]interface{}{
			"i-named":   map[string]interface{}{"service_id": "redis", "plan_id": "small"},
			"i-unnamed": map[string]interface{}{"service_id": "redis", "plan_id": "small", "deployment_name": "small-i-unnamed"},
			"i-nodata":  map[string]interface{}{"service_id": "redis", "plan_id": "big"},
			"i-noplan":  map[string]interface{}{"service_id": "redis"},
		},
		records: map[string]map[string]interface{}{
			"i-named": {"instance_name": "my-cache", "deployment_name": "recorded-dep", "password": "must-not-leak"},
		},
	}

	plans := func() map[string]services.Plan {
		return map[string]services.Plan{"redis/small": {ID: "small", Name: "Small", Service: &services.Service{Name: "Redis"}}}
	}

	refs, err := instanceLister(plans, src)(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	deployments := map[string]string{}

	for _, r := range refs {
		got[r.ID] = r.Name
		deployments[r.ID] = r.Deployment
	}

	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3 (the entry with no plan is skipped): %+v", len(refs), refs)
	}

	if got["i-named"] != "my-cache" || deployments["i-named"] != "recorded-dep" {
		t.Errorf("named instance got name %q deployment %q", got["i-named"], deployments["i-named"])
	}

	if got["i-unnamed"] != "small-i-unnamed" {
		t.Errorf("an instance with no recorded name should fall back to its deployment, got %q", got["i-unnamed"])
	}

	if got["i-nodata"] != "big-i-nodata" {
		t.Errorf("fallback for an instance with no record = %q, want big-i-nodata", got["i-nodata"])
	}

	if refs[0].ServiceName != "Redis" && refs[1].ServiceName != "Redis" {
		t.Errorf("service names missing: %+v", refs)
	}
}

package bosh_test

import (
	"reflect"
	"strings"
	"testing"

	"blacksmith/internal/bosh"
)

const (
	planOneAz       = "azs:\n- name: z1\n"
	planBrokerName  = "blacksmith"
	planCompilation = "compilation"
	// The director stores its default cloud config under this name.
	planDefaultName = "default"
)

const planBrokerContent = `
azs:
- name: z1
vm_types:
- name: small
  cloud_properties: {size: 1}
compilation:
  workers: 2
`

type planCase struct {
	name     string
	existing []bosh.CloudConfigEntry
	content  string
	action   bosh.CloudConfigAction
	holder   string
	overlaps []bosh.CloudConfigOverlap
}

func planWriteAndSkipCases() []planCase {
	return []planCase{
		{name: "no other configs", content: planBrokerContent, action: bosh.CloudConfigWrite},
		{
			name:     "other config with different names and no compilation",
			existing: []bosh.CloudConfigEntry{{Name: planDefaultName, Content: "azs:\n- name: z9\nvm_types:\n- name: big\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigWrite,
		},
		{
			name:     "the default holds the same value in another order",
			existing: []bosh.CloudConfigEntry{{Name: planDefaultName, Content: "compilation: {workers: 2}\nvm_types: [{cloud_properties: {size: 1}, name: small}]\nazs:\n  - {name: z1}\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigSkipIdentical,
			holder:   planDefaultName,
		},
		{
			name:     "a named config holds the same value",
			existing: []bosh.CloudConfigEntry{{Name: "other", Content: planBrokerContent}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigSkipIdentical,
			holder:   "other",
		},
		{
			name:     "the broker's own config is not another config",
			existing: []bosh.CloudConfigEntry{{Name: planBrokerName, Content: planBrokerContent}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigWrite,
		},
		{
			name:     "the default holds a one-az config exactly",
			existing: []bosh.CloudConfigEntry{{Name: planDefaultName, Content: planOneAz}},
			content:  planOneAz,
			action:   bosh.CloudConfigSkipIdentical,
			holder:   planDefaultName,
		},
	}
}

func planRefuseCases() []planCase {
	return []planCase{
		{
			name:     "shared az in a larger config",
			existing: []bosh.CloudConfigEntry{{Name: planDefaultName, Content: "azs:\n- name: z1\n- name: z2\n"}},
			content:  "azs:\n- name: z1\nnetworks:\n- name: n\n",
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: planDefaultName, Key: "azs", Name: "z1"}},
		},
		{
			name:     "shared vm type",
			existing: []bosh.CloudConfigEntry{{Name: "x", Content: "vm_types:\n- name: small\n"}},
			content:  "vm_types:\n- name: small\n- name: tiny\n",
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: "x", Key: "vm_types", Name: "small"}},
		},
		{
			name:     "shared disk type",
			existing: []bosh.CloudConfigEntry{{Name: "x", Content: "disk_types:\n- name: d\n- name: e\n"}},
			content:  "disk_types:\n- name: d\n- name: f\n",
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: "x", Key: "disk_types", Name: "d"}},
		},
		{
			name:     "shared network",
			existing: []bosh.CloudConfigEntry{{Name: "x", Content: "networks:\n- name: n\n- name: o\n"}},
			content:  "networks:\n- name: n\n- name: p\n",
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: "x", Key: "networks", Name: "n"}},
		},
		{
			name:     "shared vm extension",
			existing: []bosh.CloudConfigEntry{{Name: "x", Content: "vm_extensions:\n- name: e\n- name: f\n"}},
			content:  "vm_extensions:\n- name: e\n- name: g\n",
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: "x", Key: "vm_extensions", Name: "e"}},
		},
		{
			name:     "second compilation block",
			existing: []bosh.CloudConfigEntry{{Name: planDefaultName, Content: "compilation: {workers: 5}\nazs: [{name: z9}]\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: planDefaultName, Key: planCompilation}},
		},
		{
			name: "overlaps in two configs are all reported",
			existing: []bosh.CloudConfigEntry{
				{Name: "a", Content: "azs: [{name: z1}]\n"},
				{Name: "b", Content: "vm_types: [{name: small}]\ncompilation: {}\n"},
			},
			content: planBrokerContent,
			action:  bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{
				{Config: "a", Key: "azs", Name: "z1"},
				{Config: "b", Key: "vm_types", Name: "small"},
				{Config: "b", Key: planCompilation},
			},
		},
	}
}

func planBlankAndUnreadableCases() []planCase {
	return []planCase{
		{
			name:     "a null compilation value still counts as defining compilation",
			existing: []bosh.CloudConfigEntry{{Name: "x", Content: "compilation:\nazs: [{name: z9}]\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: "x", Key: planCompilation}},
		},
		{
			name:     "a config with an empty name is another config",
			existing: []bosh.CloudConfigEntry{{Name: "", Content: "compilation: {workers: 5}\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigRefuse,
			overlaps: []bosh.CloudConfigOverlap{{Config: "", Key: planCompilation}},
		},
		{
			name:     "blank other config overlaps nothing",
			existing: []bosh.CloudConfigEntry{{Name: planDefaultName, Content: "---\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigWrite,
		},
		{
			name:     "an unparseable director config writes nothing",
			existing: []bosh.CloudConfigEntry{{Name: "bad", Content: "azs: [unclosed"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigSkipUnreadable,
		},
		{
			name:     "a director config that is a list writes nothing",
			existing: []bosh.CloudConfigEntry{{Name: "odd", Content: "- a\n"}},
			content:  planBrokerContent,
			action:   bosh.CloudConfigSkipUnreadable,
		},
		{name: "unparseable broker content writes nothing", content: "azs: [unclosed", action: bosh.CloudConfigSkipUnreadable},
		{name: "broker content that is a list writes nothing", content: "- a\n", action: bosh.CloudConfigSkipUnreadable},
	}
}

func TestPlanCloudConfigWriteDecidesByMeaning(t *testing.T) {
	t.Parallel()

	tests := append(planWriteAndSkipCases(), planRefuseCases()...)
	tests = append(tests, planBlankAndUnreadableCases()...)

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			plan := bosh.PlanCloudConfigWrite(testCase.existing, testCase.content)

			if plan.Action != testCase.action {
				t.Fatalf("expected action %d, got %d (%+v)", testCase.action, plan.Action, plan)
			}

			if plan.Holder != testCase.holder {
				t.Errorf("expected holder %q, got %q", testCase.holder, plan.Holder)
			}

			if !reflect.DeepEqual(plan.Overlaps, testCase.overlaps) {
				t.Errorf("expected overlaps %v, got %v", testCase.overlaps, plan.Overlaps)
			}

			if plan.Action == bosh.CloudConfigSkipUnreadable && strings.TrimSpace(plan.Reason) == "" {
				t.Error("expected a reason for an unreadable config")
			}
		})
	}
}

func TestIsEmptyCloudConfig(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"", "---", "{}", "--- {}", "[]", "--- []", "~", "  \n", "\t", " \t\n\t ", "# only a comment\n"} {
		empty, err := bosh.IsEmptyCloudConfig(content)
		if err != nil || !empty {
			t.Errorf("expected %q to be empty, got %v, %v", content, empty, err)
		}
	}

	empty, err := bosh.IsEmptyCloudConfig("azs: []\n")
	if err != nil || empty {
		t.Errorf("expected content to be non-empty, got %v, %v", empty, err)
	}

	_, err = bosh.IsEmptyCloudConfig("azs: [unclosed")
	if err == nil {
		t.Error("expected a YAML error")
	}
}

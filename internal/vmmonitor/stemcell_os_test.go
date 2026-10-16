package vmmonitor_test

import (
	"testing"

	"blacksmith/internal/vmmonitor"
)

// The monitor reads a stemcell's OS from its name. Any ubuntu codename is
// recognised, including one that does not exist yet, so a new stemcell
// line needs no change to the monitor.
func TestExtractOSFromStemcellName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stemcell string
		want     string
	}{
		{"noble with the agent suffix", "bosh-openstack-kvm-ubuntu-noble-go_agent", "ubuntu-noble"},
		{"noble without the agent suffix", "bosh-openstack-kvm-ubuntu-noble", "ubuntu-noble"},
		{"resolute with the agent suffix", "bosh-openstack-kvm-ubuntu-resolute-go_agent", "ubuntu-resolute"},
		{"resolute without the agent suffix", "bosh-openstack-kvm-ubuntu-resolute", "ubuntu-resolute"},
		{"an unknown future codename", "bosh-aws-xen-hvm-ubuntu-zesty-go_agent", "ubuntu-zesty"},
		{"jammy", "bosh-google-kvm-ubuntu-jammy-go_agent", "ubuntu-jammy"},
		{"bionic", "bosh-warden-boshlite-ubuntu-bionic-go_agent", "ubuntu-bionic"},
		{"xenial", "bosh-vsphere-esxi-ubuntu-xenial-go_agent", "ubuntu-xenial"},
		{"trusty", "bosh-aws-xen-ubuntu-trusty-go_agent", "ubuntu-trusty"},
		{"centos 7", "bosh-vsphere-esxi-centos-7-go_agent", "centos-7"},
		{"windows 2019", "bosh-vsphere-esxi-windows2019-go_agent", "windows2019"},
		{"windows 2016", "bosh-vsphere-esxi-windows2016-go_agent", "windows2016"},
		{"a name with no OS in it", "bosh-warden-boshlite-go_agent", ""},
		{"ubuntu with no codename", "bosh-warden-boshlite-ubuntu", ""},
		{"an empty name", "", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := vmmonitor.ExtractOSFromStemcellName(test.stemcell)
			if got != test.want {
				t.Errorf("the OS of %q was %q, want %q", test.stemcell, got, test.want)
			}
		})
	}
}

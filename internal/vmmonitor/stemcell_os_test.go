package vmmonitor

import "testing"

func TestExtractOSFromStemcellName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"bosh-google-kvm-ubuntu-jammy-go_agent", "ubuntu-jammy"},
		{"bosh-aws-xen-hvm-ubuntu-noble-go_agent", "ubuntu-noble"},          // the reported bug
		{"bosh-vsphere-esxi-ubuntu-oracular-go_agent", "ubuntu-oracular"},   // future codename via fallback
		{"bosh-warden-boshlite-ubuntu-bionic-go_agent", "ubuntu-bionic"},
		{"bosh-aws-xen-hvm-windows2019-go_agent", "windows2019"},
		{"bosh-openstack-kvm-centos-7-go_agent", "centos-7"},
		{"some-unrecognized-stemcell", ""},
	}

	for _, c := range cases {
		if got := extractOSFromStemcellName(c.name); got != c.want {
			t.Errorf("extractOSFromStemcellName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

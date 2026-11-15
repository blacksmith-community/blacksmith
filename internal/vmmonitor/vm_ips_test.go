package vmmonitor_test

import (
	"reflect"
	"testing"

	"blacksmith/internal/vmmonitor"
)

const ipsKey = "ips"

// The status the monitor cached for an instance names the address of each
// of its VMs, so the list can find an instance by IP without asking BOSH.
func TestGetServiceVMStatusReportsTheCachedVMAddresses(t *testing.T) {
	t.Parallel()

	vault := &indexVault{records: map[string]map[string]interface{}{
		"inst-1/vm_status": {
			"status": vmStatusRunning,
			"vms": []interface{}{
				map[string]interface{}{ipsKey: []interface{}{"10.0.0.5", "fd00::5"}},
				map[string]interface{}{ipsKey: []interface{}{"10.0.0.6"}},
				map[string]interface{}{ipsKey: []interface{}{}},
			},
		},
		"inst-2/vm_status": {"status": vmStatusRunning},
	}}
	monitor := vmmonitor.NewWithVault(vault, nil, noClaims{})

	status, err := monitor.GetServiceVMStatus(t.Context(), "inst-1")
	if err != nil {
		t.Fatalf("reading the VM status: %v", err)
	}

	want := []string{"10.0.0.5", "fd00::5", "10.0.0.6"}
	if !reflect.DeepEqual(status.IPs, want) {
		t.Errorf("the status lists the addresses %v, want %v", status.IPs, want)
	}

	status, err = monitor.GetServiceVMStatus(t.Context(), "inst-2")
	if err != nil {
		t.Fatalf("reading the VM status: %v", err)
	}

	if len(status.IPs) != 0 {
		t.Errorf("a status with no VMs lists the addresses %v, want none", status.IPs)
	}
}

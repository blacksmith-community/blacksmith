package upgrade

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A node a worker has claimed is still pending while the worker lists the
// VMs, connects, and checks sudo, so a reader of the task sees it in
// flight. The mark is never saved, so a restart finds the node free, and it
// is gone once the worker gives the claim back.
func TestAClaimedNodeReadsInFlightAndIsNeverSaved(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	inst := &fix.task.Instances[0]

	err := fix.manager.claimCommandNode(inst, 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	read, err := fix.manager.GetTask(fix.task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	nodes := read.Instances[0].Command.VMs
	if !nodes[0].InFlight || nodes[1].InFlight {
		t.Errorf("the nodes read in flight %v and %v, want only the claimed one", nodes[0].InFlight, nodes[1].InFlight)
	}

	raw, err := json.Marshal(read.Instances[0].Command)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if got := strings.Count(string(raw), `"in_flight":true`); got != 1 {
		t.Errorf("the record marks %d nodes in flight, want 1", got)
	}

	err = fix.manager.persistTask(context.Background(), fix.task)
	if err != nil {
		t.Fatalf("persist: %v", err)
	}

	for vm, node := range fix.lastSaved().Instances[0].Command.VMs {
		if node.InFlight {
			t.Errorf("the saved record marks node %d in flight", vm)
		}
	}

	fix.manager.releaseCommandNode(inst, 0)

	read, err = fix.manager.GetTask(fix.task.ID)
	if err != nil {
		t.Fatalf("get after release: %v", err)
	}

	if read.Instances[0].Command.VMs[0].InFlight {
		t.Error("the node still reads in flight once its claim is given back")
	}

	fix.assertNoCommandText()
}

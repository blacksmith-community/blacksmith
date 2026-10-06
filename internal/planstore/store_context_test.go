package planstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"blacksmith/internal/planstore"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
)

// StorePlanReferences is called from a provision request that may end before
// the write does, so the write has to land on a context that has ended.
func TestStorePlanReferencesLandsAfterTheContextEnds(t *testing.T) {
	t.Parallel()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start the in-process Vault: %v", err)
	}

	script := filepath.Join(t.TempDir(), "init")

	err = os.WriteFile(script, []byte("#!/bin/bash\nexit 0\n"), 0o600)
	if err != nil {
		t.Fatalf("failed to write the init script: %v", err)
	}

	store := planstore.New(internalVault.New(server.Addr, server.RootToken, true), nil)

	ended, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.StorePlanReferences(ended, "plan-refs-instance", services.Plan{ID: "standalone", Name: "standalone", InitScriptPath: script})
	if err != nil {
		t.Fatalf("StorePlanReferences failed on a context that had ended: %v", err)
	}

	stored, err := server.ReadSecret("plan-refs-instance/plans/sha256")
	if err != nil {
		t.Fatalf("the plan references were not stored: %v", err)
	}

	if _, ok := stored["init"]; !ok {
		t.Fatalf("the stored references have no init entry: %v", stored)
	}
}

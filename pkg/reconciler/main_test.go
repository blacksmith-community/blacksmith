package reconciler_test

import (
	"os"
	"testing"

	testutil "blacksmith/pkg/testutil"
)

// testSuite holds the shared test resources for all reconciler tests.
type testSuite struct {
	vault *testutil.VaultDevServer
}

//nolint:gochecknoglobals // Test suite needs shared state across all tests
var suite = &testSuite{}

func TestMain(m *testing.M) {
	// Start a single shared in-memory Vault for all reconciler tests
	vaultServer, err := testutil.NewVaultDevServer(nil)
	if err != nil {
		// cannot proceed without a test vault
		panic(err)
	}

	suite.vault = vaultServer
	code := m.Run()

	if suite.vault != nil {
		suite.vault.Close()
	}

	os.Exit(code)
}

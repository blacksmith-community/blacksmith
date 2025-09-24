package cf_test

import (
	"os"
	"testing"

	"blacksmith/pkg/testutil"
)

type testSuite struct {
	vault *testutil.VaultDevServer
}

func TestMain(m *testing.M) {
	suite := &testSuite{}

	vaultServer, err := testutil.NewVaultDevServer(nil)
	if err != nil {
		panic(err)
	}

	suite.vault = vaultServer
	code := m.Run()

	if suite.vault != nil {
		suite.vault.Close()
	}

	os.Exit(code)
}

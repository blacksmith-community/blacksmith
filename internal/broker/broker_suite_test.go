package broker_test

import (
	"testing"
	"time"

	"blacksmith/internal/broker"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// testSuite holds the shared test resources for all broker integration tests.
type testSuite struct {
	vault *testutil.VaultDevServer
}

//nolint:gochecknoglobals // Test suite needs shared state across all tests
var suite = &testSuite{}

var _ = BeforeSuite(func() {
	// Speed up tests by reducing timeouts and retry delays
	broker.SetDefaultDeleteTimeout(1 * time.Second)
	broker.SetDefaultRetryBaseDelay(10 * time.Millisecond)

	var err error
	suite.vault, err = testutil.NewVaultDevServer(nil)
	if err != nil {
		Fail("failed to start suite Vault: " + err.Error())
	}
})

var _ = AfterSuite(func() {
	if suite.vault != nil {
		suite.vault.Close()
	}
})

func TestBroker(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Broker Suite")
}

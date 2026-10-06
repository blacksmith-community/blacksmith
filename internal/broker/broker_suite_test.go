package broker_test

import (
	"context"
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

// backgroundStopTimeout bounds the wait for the broker's goroutines to return
// once a spec has stopped them.
const backgroundStopTimeout = 20 * time.Second

// Every spec builds its own Vault value against the one suite Vault, and the
// index lock belongs to a Vault value, so a goroutine left over from one spec
// can drop another spec's index write. No goroutine of package broker may
// outlive the spec that started it. This runs after the spec's own AfterEach
// blocks.
var _ = AfterEach(func() {
	expectNoBrokerGoroutines()
})

// expectNoBrokerGoroutines waits until no goroutine of package broker is
// running, and fails the spec with their stacks when some still are.
func expectNoBrokerGoroutines() {
	Eventually(broker.BackgroundGoroutines, backgroundStopTimeout, 10*time.Millisecond).Should(BeEmpty(),
		"a goroutine of package broker outlived its spec")
}

// stopBrokerGoroutines ends the service context a spec gave its broker, which
// stops the provision, deprovision, and monitor goroutines that derive from
// it, and then waits for every goroutine of package broker to return.
func stopBrokerGoroutines(stopService context.CancelFunc) {
	stopService()
	expectNoBrokerGoroutines()
}

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

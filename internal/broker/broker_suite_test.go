package broker_test

import (
	"context"
	"testing"
	"time"

	"blacksmith/internal/broker"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo/v2"
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
	broker.SetRabbitMQUserRetryBaseDelay(10 * time.Millisecond)

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

// stopAndWaitForBackground ends the service context of brokerInstance and
// waits for the goroutines it tracks, so none outlives the test that
// started it and keeps writing to a Vault the test has stopped. The suite
// fails a spec while any goroutine of package broker runs, so a plain
// test's leftover goroutine would fail a spec that runs beside it in the
// same process. The wait does not end with ctx, which a test has often
// canceled by the time its cleanups run.
func stopAndWaitForBackground(ctx context.Context, brokerInstance *broker.Broker, stopService context.CancelFunc) {
	stopService()

	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), backgroundStopTimeout)
	defer cancel()

	brokerInstance.WaitForBackground(waitCtx)
}

// shortenVaultRetryWaits keeps the retries of the API client of v and cuts
// the wait between them to a millisecond, so a spec against an unreachable
// or failing Vault does not wait out the client's backoff of a second or
// more between tries.
func shortenVaultRetryWaits(v *internalVault.Vault) {
	client, err := v.GetAPIClient()
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	client.SetMinRetryWait(time.Millisecond)
	client.SetMaxRetryWait(time.Millisecond)
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

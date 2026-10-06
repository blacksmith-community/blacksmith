package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// errorLines records the messages logged at Error, the level a failed index
// read is reported at.
type errorLines struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (e *errorLines) Error(msg string, args ...interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.lines = append(e.lines, fmt.Sprintf(msg, args...))
}

func (e *errorLines) Errorf(msg string, args ...interface{}) { e.Error(msg, args...) }

func (e *errorLines) Named(string) logger.Logger { return e }

// mentioning returns the logged lines that contain text.
func (e *errorLines) mentioning(text string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	var matched []string

	for _, line := range e.lines {
		if strings.Contains(line, text) {
			matched = append(matched, line)
		}
	}

	return matched
}

// The accept path builds an instance's index entry from the entry already
// stored, then writes it. When that first read fails, the broker must not write
// an entry built from nothing, because the write would drop deployment_name,
// created_at, and every other stored field. Only the first read fails here, so
// the write's own read of the index would have worked.
var _ = Describe("Index read errors in the broker", func() {
	const (
		indexPath = "secret/data/db"
		serviceID = "redis-service"
		planID    = "small-plan"
	)

	var (
		ctx        context.Context
		proxy      *testutil.VaultFaultProxy
		b          *broker.Broker
		instanceID string
		stored     map[string]interface{}
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("index-read-errors-%d", time.Now().UnixNano())

		var err error

		proxy, err = testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		b = &broker.Broker{
			Plans:  map[string]services.Plan{},
			BOSH:   testutil.NewMockBOSHDirector(),
			Vault:  internalVault.New(proxy.Addr, suite.vault.RootToken, true),
			Config: &config.Config{},
		}

		stored = map[string]interface{}{
			"service_id":      serviceID,
			"plan_id":         planID,
			"deployment_name": planID + "-" + instanceID,
			"created_at":      "2026-01-01T00:00:00Z",
			"status":          "succeeded",
		}

		direct := internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		Expect(direct.Index(ctx, instanceID, stored)).To(Succeed())

		proxy.FailReads(indexPath, http.StatusForbidden, "permission denied", 1)
	})

	AfterEach(func() {
		proxy.Close()

		direct := internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		_ = direct.Index(ctx, instanceID, nil)
	})

	expectEntryUntouched := func() {
		Expect(proxy.WritesTo("/db")).To(BeEmpty(), "the broker wrote the index after it failed to read the entry")

		index, err := suite.vault.ReadSecret("db")
		Expect(err).ToNot(HaveOccurred())
		Expect(index).To(HaveKey(instanceID))

		entry, ok := index[instanceID].(map[string]interface{})
		Expect(ok).To(BeTrue())

		for key, value := range stored {
			Expect(entry).To(HaveKeyWithValue(key, value))
		}
	}

	provisionRequest := func() osbapi.ProvisionRequest {
		return osbapi.ProvisionRequest{ServiceID: serviceID, PlanID: planID}
	}

	// Deprovision stops, because its record of the request is what the rest of
	// the delete relies on.
	It("stops the deprovision request without writing the index", func() {
		err := b.RecordDeprovisionRequest(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID})
		Expect(err).To(HaveOccurred())
		expectEntryUntouched()
	})

	// A provision stops, because the first record of the request is what
	// the plan limit counts.
	It("stops the first provision record without writing the index", func() {
		err := b.RecordInitialRequest(ctx, instanceID, provisionRequest())
		Expect(err).To(MatchError(broker.ErrFailedToTrackServiceRequestInVault))
		expectEntryUntouched()
	})

	It("stops the provision status update without writing the index", func() {
		plan := &services.Plan{ID: planID}

		err := b.UpdateInstanceStatus(ctx, instanceID, provisionRequest(), plan, "provisioning")
		Expect(err).To(MatchError(broker.ErrFailedToUpdateServiceStatusInVault))
		expectEntryUntouched()
	})

	// Recording the deployment name is non-fatal today, and stays so. It
	// logs and leaves the entry as it was.
	It("skips the deployment name write without failing the provision", func() {
		b.StoreDeploymentNameInIndex(ctx, instanceID, "a-different-name")
		expectEntryUntouched()
	})

	// The step that stops is the one that reports it, so a failed read
	// shows up once in the log, with that step named.
	Context("the log of a failed read", func() {
		var (
			logged   *errorLines
			previous logger.Logger
		)

		BeforeEach(func() {
			logged = &errorLines{NoOpLogger: &logger.NoOpLogger{}}
			previous = logger.Get()

			logger.Set(logged)
		})

		AfterEach(func() {
			logger.Set(previous)
		})

		readFailure := func() string { return "index entry for " + instanceID }

		It("reports a failed first provision record once", func() {
			Expect(b.RecordInitialRequest(ctx, instanceID, provisionRequest())).ToNot(Succeed())

			Expect(logged.mentioning(readFailure())).To(HaveLen(1))
			Expect(logged.mentioning(readFailure())[0]).To(ContainSubstring("failed to record service request in vault"))
		})

		It("reports a failed status update once", func() {
			plan := &services.Plan{ID: planID}

			Expect(b.UpdateInstanceStatus(ctx, instanceID, provisionRequest(), plan, "provisioning")).ToNot(Succeed())

			Expect(logged.mentioning(readFailure())).To(HaveLen(1))
			Expect(logged.mentioning(readFailure())[0]).To(ContainSubstring("failed to update service status in vault index"))
		})

		It("reports a skipped deployment name write once", func() {
			b.StoreDeploymentNameInIndex(ctx, instanceID, "a-different-name")

			Expect(logged.mentioning(readFailure())).To(HaveLen(1))
			Expect(logged.mentioning(readFailure())[0]).To(ContainSubstring("failed to record deployment name in vault index"))
		})

		// Deprovision logs the error this returns, so the record itself
		// logs nothing.
		It("leaves the report of a failed deprovision record to Deprovision", func() {
			err := b.RecordDeprovisionRequest(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID})
			Expect(err).To(MatchError(ContainSubstring(readFailure())))

			Expect(logged.mentioning(readFailure())).To(BeEmpty())
		})
	})

	It("writes the merged entry when the index reads", func() {
		proxy.ClearFaults()

		Expect(b.RecordInitialRequest(ctx, instanceID, provisionRequest())).To(Succeed())

		index, err := suite.vault.ReadSecret("db")
		Expect(err).ToNot(HaveOccurred())

		entry, ok := index[instanceID].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(entry).To(HaveKeyWithValue("deployment_name", planID+"-"+instanceID), "the merge must keep the stored fields")
		Expect(entry).To(HaveKeyWithValue("status", "request_received"))
	})
})

package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// indexReadGate forwards every request to a Vault server and holds the answer
// to the first read of the index once armed, until the spec releases it. A spec uses it to
// hold a writer between its read of the index and its save, the window in
// which another writer's change used to be lost.
type indexReadGate struct {
	Addr string

	server  *httptest.Server
	mu      sync.Mutex
	armed   bool
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

func newIndexReadGate(target string) *indexReadGate {
	targetURL, err := url.Parse(target)
	Expect(err).ToNot(HaveOccurred())

	gate := &indexReadGate{arrived: make(chan struct{}), release: make(chan struct{})}
	reverse := httputil.NewSingleHostReverseProxy(targetURL)

	gate.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !gate.stops(request) {
			reverse.ServeHTTP(writer, request)

			return
		}

		// The read reaches Vault now, and its answer is held, so the
		// writer goes on with the index as it was before the hold.
		answer := httptest.NewRecorder()
		reverse.ServeHTTP(answer, request)
		close(gate.arrived)

		select {
		case <-gate.release:
		case <-request.Context().Done():
		}

		for key, values := range answer.Header() {
			writer.Header()[key] = values
		}

		writer.WriteHeader(answer.Code)
		_, _ = writer.Write(answer.Body.Bytes())
	}))
	gate.Addr = gate.server.URL

	return gate
}

// stops reports whether request is the first index read since Arm.
func (g *indexReadGate) stops(request *http.Request) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.armed || request.Method != http.MethodGet || !strings.HasSuffix(request.URL.Path, "/secret/data/db") {
		return false
	}

	g.armed = false

	return true
}

// Arm makes the next index read wait for Release.
func (g *indexReadGate) Arm() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.armed = true
}

// Release lets the held read through. It is safe to call more than once.
func (g *indexReadGate) Release() {
	g.once.Do(func() { close(g.release) })
}

func (g *indexReadGate) Close() {
	g.Release()
	g.server.Close()
}

// The vm-monitor marks an instance deleted through the broker's Vault while
// the broker's own writers merge fields into the same index entry. Each of
// those writers reads the entry and writes it back, and the mark must survive
// a writer whose read came before it.
var _ = Describe("Index entry writes that overlap a deleted mark", func() {
	const (
		serviceID = "redis-service"
		planID    = "small-plan"
	)

	var (
		ctx        context.Context
		gate       *indexReadGate
		b          *broker.Broker
		instanceID string
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("index-entry-lock-%d", time.Now().UnixNano())
		gate = newIndexReadGate(suite.vault.Addr)

		b = &broker.Broker{
			Plans:  map[string]services.Plan{},
			BOSH:   testutil.NewMockBOSHDirector(),
			Vault:  internalVault.New(gate.Addr, suite.vault.RootToken, true),
			Config: &config.Config{},
		}

		Expect(b.Vault.Index(ctx, instanceID, map[string]interface{}{
			"service_id":      serviceID,
			"plan_id":         planID,
			"deployment_name": planID + "-" + instanceID,
			"status":          "succeeded",
		})).To(Succeed())
	})

	AfterEach(func() {
		gate.Close()

		direct := internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		_ = direct.Index(ctx, instanceID, nil)
		_ = suite.vault.DeleteSecret(instanceID + "/metadata")
	})

	// overlapWithMark runs write with its index read held at the gate, marks
	// the instance deleted meanwhile, and returns the entry both left behind.
	// With the lock covering the writer's read, the mark waits for the
	// writer, so the hold ends after a short wait. Without it, the mark lands
	// during the hold, and the writer then saves the entry it read before.
	overlapWithMark := func(write func() error) map[string]interface{} {
		gate.Arm()

		writeErr := make(chan error, 1)

		go func() { writeErr <- write() }()

		Eventually(gate.arrived, 10*time.Second).Should(BeClosed(), "the writer never read the index")

		markErr := make(chan error, 1)

		go func() { markErr <- b.Vault.MarkInstanceDeleted(ctx, instanceID) }()

		select {
		case err := <-markErr:
			markErr <- err
		case <-time.After(500 * time.Millisecond):
		}

		gate.Release()

		Eventually(writeErr, 10*time.Second).Should(Receive(BeNil()))
		Eventually(markErr, 10*time.Second).Should(Receive(BeNil()))

		index, err := suite.vault.ReadSecret("db")
		Expect(err).ToNot(HaveOccurred())

		entry, ok := index[instanceID].(map[string]interface{})
		Expect(ok).To(BeTrue(), "the instance's index entry is gone")

		return entry
	}

	expectMarked := func(entry map[string]interface{}) {
		Expect(entry).To(HaveKey("deleted_at"), "the deleted mark was lost")
		Expect(entry).To(HaveKeyWithValue("deleted_by", "vm-monitor"))
		Expect(entry).To(HaveKey("deletion_reason"))
		Expect(entry).To(HaveKeyWithValue("deployment_name", planID+"-"+instanceID))
	}

	It("keeps the mark when created_at is recorded at the same time", func() {
		entry := overlapWithMark(func() error {
			return b.UpdateInstanceTimestamp(ctx, instanceID)
		})

		expectMarked(entry)
		Expect(entry).To(HaveKey("created_at"))
	})
})

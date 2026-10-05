package broker_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// sweepCredHub is a CredHub fake that lists from a fixed set of names and
// records every find and delete. FindByPath can be made to block.
type sweepCredHub struct {
	mu      sync.Mutex
	names   []string
	finds   []string
	deleted []string
	block   chan struct{}
}

func (f *sweepCredHub) FindByPath(_ context.Context, path string) ([]string, error) {
	f.mu.Lock()
	f.finds = append(f.finds, path)
	block := f.block
	f.mu.Unlock()

	if block != nil {
		<-block
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var matched []string

	for _, name := range f.names {
		if strings.HasPrefix(name, path) {
			matched = append(matched, name)
		}
	}

	return matched, nil
}

func (f *sweepCredHub) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, name)

	return nil
}

func (f *sweepCredHub) Finds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.finds...)
}

func (f *sweepCredHub) Deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	deleted := append([]string(nil), f.deleted...)
	sort.Strings(deleted)

	return deleted
}

// sweepClock is a settable clock for the sweep's once-an-hour rule.
type sweepClock struct {
	now atomic.Int64
}

func (c *sweepClock) Now() time.Time { return time.Unix(0, c.now.Load()) }

func (c *sweepClock) Advance(d time.Duration) { c.now.Add(int64(d)) }

var _ = Describe("CredHub sweep", func() {
	const (
		directorName = "lab-bosh"
		planID       = "valkey-standalone"
		brokerDeploy = "lab-blacksmith"
	)

	var (
		ctx            context.Context
		brokerInstance *broker.Broker
		vaultClient    *internalVault.Vault
		director       *testutil.ScriptedBOSHDirector
		fakeCredHub    *sweepCredHub
		clock          *sweepClock
		capture        *captureLogger
		previousLogger logger.Logger
		existing       map[string]bool
		existingMu     sync.Mutex
		seededIDs      []string
		live           map[string]bool
		blockers       []*recordingCleaner
	)

	deploymentFor := func(guid string) string { return planID + "-" + guid }

	prefixFor := func(deployment string) string { return "/" + directorName + "/" + deployment + "/" }

	requestDeprovision := func(guid string, ago time.Duration) {
		Expect(vaultClient.Put(ctx, guid+"/metadata", map[string]interface{}{
			"delete_requested_at": time.Now().Add(-ago).Format(time.RFC3339),
		})).To(Succeed())
	}

	newGUID := func() string {
		guid := cleanupGUID()
		seededIDs = append(seededIDs, guid)

		return guid
	}

	// provenOrphan lists two variables for a dead deployment whose deprovision
	// Cloud Foundry requested three hours ago.
	provenOrphan := func() string {
		guid := newGUID()
		requestDeprovision(guid, 3*time.Hour)
		fakeCredHub.names = append(fakeCredHub.names,
			prefixFor(deploymentFor(guid))+"valkey_password",
			prefixFor(deploymentFor(guid))+"valkey_ca")

		return guid
	}

	useMode := func(mode string) {
		brokerInstance.Config.CredHub.Cleanup.Sweep = mode
	}

	useRealCleaner := func() {
		policy := func() credhub.Policy {
			return credhub.Policy{DirectorName: directorName, PlanIDs: brokerInstance.CatalogPlanIDs(), ProtectedDeployments: []string{brokerDeploy}}
		}
		brokerInstance.CredentialCleaner = credhub.NewCleaner(fakeCredHub, director, policy, logger.Get())
	}

	useBlockingCleaner := func(behaviour cleanerBehaviour) *recordingCleaner {
		cleaner := newRecordingCleaner(behaviour)
		blockers = append(blockers, cleaner)
		brokerInstance.CredentialCleaner = cleaner

		return cleaner
	}

	sweepAndWait := func() {
		brokerInstance.SweepOrphanedCredentials(ctx, live)
		brokerInstance.WaitForCredentialCleanups()
	}

	BeforeEach(func() {
		ctx = context.Background()
		seededIDs = nil
		blockers = nil
		live = map[string]bool{brokerDeploy: true}

		capture = newCaptureLogger()
		previousLogger = logger.Get()

		logger.Set(capture)

		clock = &sweepClock{}
		clock.now.Store(time.Now().UnixNano())

		existingMu.Lock()
		existing = map[string]bool{brokerDeploy: true}
		existingMu.Unlock()

		director = testutil.NewScriptedBOSHDirector()
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{Name: directorName}, nil }
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			existingMu.Lock()
			defer existingMu.Unlock()

			if existing[name] {
				return &bosh.DeploymentDetail{Name: name, Manifest: "name: " + name}, nil
			}

			return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
		}
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		fakeCredHub = &sweepCredHub{names: []string{prefixFor(brokerDeploy) + "blacksmith_password"}}

		brokerInstance = &broker.Broker{
			Plans: map[string]services.Plan{
				"valkey/standalone": {ID: planID, Name: "standalone", Service: &services.Service{ID: "valkey", Name: "valkey"}},
			},
			BOSH:  director,
			Vault: vaultClient,
			Config: &config.Config{CredHub: config.CredHubConfig{
				DirectorName: directorName,
				Cleanup: config.CredHubCleanupConfig{
					Enabled: true, Sweep: config.CredHubSweepOff, ProtectedDeployments: []string{brokerDeploy},
				},
			}},
			InstanceLocks:    make(map[string]*sync.Mutex),
			CredentialFinder: fakeCredHub,
		}
		brokerInstance.SetCredentialSweepClock(clock.Now)
		useRealCleaner()
	})

	AfterEach(func() {
		for _, cleaner := range blockers {
			cleaner.Release()
		}

		fakeCredHub.mu.Lock()
		if fakeCredHub.block != nil {
			select {
			case <-fakeCredHub.block:
			default:
				close(fakeCredHub.block)
			}
		}
		fakeCredHub.mu.Unlock()

		brokerInstance.WaitForCredentialCleanups()
		logger.Set(previousLogger)

		for _, guid := range seededIDs {
			_ = vaultClient.Index(ctx, guid, nil)
			_ = vaultClient.Delete(ctx, guid+"/metadata")
		}
	})

	It("makes no CredHub call at all in mode off", func() {
		provenOrphan()
		useMode(config.CredHubSweepOff)

		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(BeEmpty())
		Expect(fakeCredHub.Deleted()).To(BeEmpty())
	})

	It("makes no CredHub call when no cleaner is configured", func() {
		provenOrphan()
		useMode(config.CredHubSweepDelete)

		brokerInstance.CredentialCleaner = nil

		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(BeEmpty())
		Expect(brokerInstance.CredentialSweepMode()).To(Equal(reconciler.CredentialSweepModeDisabled))
	})

	It("makes no CredHub call when the deployment scan came back empty", func() {
		provenOrphan()
		useMode(config.CredHubSweepDelete)

		live = map[string]bool{}

		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(BeEmpty())
	})

	It("makes no CredHub call when the director's /info name differs from the configured one", func() {
		provenOrphan()
		useMode(config.CredHubSweepDelete)

		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{Name: "some-other-bosh"}, nil }

		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(BeEmpty())
		Expect(capture.output()).To(ContainSubstring("some-other-bosh"))
	})

	It("reports the configured sweep mode", func() {
		useMode(config.CredHubSweepDryRun)

		Expect(brokerInstance.CredentialSweepMode()).To(Equal(config.CredHubSweepDryRun))
	})

	It("logs the proven candidate and deletes nothing in dry-run", func() {
		guid := provenOrphan()

		useMode(config.CredHubSweepDryRun)

		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(Equal([]string{"/" + directorName + "/"}))
		Expect(fakeCredHub.Deleted()).To(BeEmpty())
		Expect(capture.output()).To(ContainSubstring("would delete 2 credentials under " + prefixFor(deploymentFor(guid)) +
			" (deployment absent, Cloud Foundry requested the deprovision at "))
	})

	It("deletes the proven candidate and nothing else in delete mode", func() {
		useMode(config.CredHubSweepDelete)

		proven := provenOrphan()

		liveGUID := newGUID()
		requestDeprovision(liveGUID, 3*time.Hour)
		live[deploymentFor(liveGUID)] = true

		noRecord := newGUID()

		reconcilerOnly := newGUID()
		Expect(vaultClient.Put(ctx, reconcilerOnly+"/metadata", map[string]interface{}{
			"deleted_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			"deleted_by": "reconciler",
			"status":     "deleted",
		})).To(Succeed())

		tooRecent := newGUID()
		requestDeprovision(tooRecent, 30*time.Minute)

		stillIndexed := newGUID()
		requestDeprovision(stillIndexed, 3*time.Hour)
		Expect(vaultClient.Index(ctx, stillIndexed, vaultPkg.Instance{
			ID: stillIndexed, ServiceID: "valkey", PlanID: planID, DeploymentName: deploymentFor(stillIndexed),
		})).To(Succeed())

		retiredPlan := newGUID()
		requestDeprovision(retiredPlan, 3*time.Hour)

		// The scan missed this deployment, but the director still has it.
		missedByScan := newGUID()
		requestDeprovision(missedByScan, 3*time.Hour)
		existingMu.Lock()
		existing[deploymentFor(missedByScan)] = true
		existingMu.Unlock()

		taskRunning := newGUID()
		requestDeprovision(taskRunning, 3*time.Hour)

		director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
			if name == deploymentFor(taskRunning) {
				return &bosh.Task{ID: 42, Description: "delete deployment", State: "processing"}, nil
			}

			return nil, nil //nolint:nilnil // no running task is a nil task without an error
		}

		for _, guid := range []string{liveGUID, noRecord, reconcilerOnly, tooRecent, stillIndexed, missedByScan, taskRunning} {
			fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(guid))+"valkey_password")
		}

		fakeCredHub.names = append(fakeCredHub.names,
			prefixFor("redis-cluster-"+retiredPlan)+"redis_password",
			"/"+directorName+"/ocfp-cf1-lab-ocf-cf/foo",
			prefixFor(deploymentFor(proven))+"nested/name",
		)

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(Equal([]string{
			prefixFor(deploymentFor(proven)) + "valkey_ca",
			prefixFor(deploymentFor(proven)) + "valkey_password",
		}))

		output := capture.output()
		Expect(output).To(ContainSubstring(deploymentFor(noRecord)))
		Expect(output).To(ContainSubstring(deploymentFor(reconcilerOnly)))
		Expect(output).To(ContainSubstring("no deprovision request on record"))
		Expect(output).To(ContainSubstring(deploymentFor(tooRecent)))
		Expect(output).To(ContainSubstring(deploymentFor(stillIndexed)))
		Expect(output).To(ContainSubstring(deploymentFor(missedByScan) + " is not a proven orphan: the director still has the deployment"))
		Expect(output).To(ContainSubstring(deploymentFor(taskRunning) + " is not a proven orphan: task 42"))
	})

	It("does not prove an orphan whose delete request predates the instance's creation, as with a reused GUID", func() {
		useMode(config.CredHubSweepDelete)

		reused := newGUID()
		Expect(vaultClient.Put(ctx, reused+"/metadata", map[string]interface{}{
			"delete_requested_at": time.Now().Add(-5 * time.Hour).Format(time.RFC3339),
			"created_at":          time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
		})).To(Succeed())

		genuine := newGUID()
		Expect(vaultClient.Put(ctx, genuine+"/metadata", map[string]interface{}{
			"created_at":          time.Now().Add(-6 * time.Hour).Format(time.RFC3339),
			"delete_requested_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
		})).To(Succeed())

		for _, guid := range []string{reused, genuine} {
			fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(guid))+"valkey_password")
		}

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(Equal([]string{prefixFor(deploymentFor(genuine)) + "valkey_password"}))

		output := capture.output()
		Expect(output).To(ContainSubstring(deploymentFor(reused) + " is not a proven orphan"))
		Expect(output).To(ContainSubstring("predates"))
		Expect(output).To(ContainSubstring("created_at"))
	})

	It("does not prove an orphan whose GUID was provisioned again after the delete request and has not completed yet", func() {
		useMode(config.CredHubSweepDelete)

		reprovisioned := newGUID()
		Expect(vaultClient.Put(ctx, reprovisioned+"/metadata", map[string]interface{}{
			"created_at":             time.Now().Add(-8 * time.Hour).Format(time.RFC3339),
			"delete_requested_at":    time.Now().Add(-5 * time.Hour).Format(time.RFC3339),
			"provision_requested_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
		})).To(Succeed())
		fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(reprovisioned))+"valkey_password")

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(BeEmpty())
		Expect(capture.output()).To(ContainSubstring("provision_requested_at"))
		Expect(capture.output()).To(ContainSubstring("predates"))
	})

	It("proves an orphan whose delete request follows its provision request and its creation", func() {
		useMode(config.CredHubSweepDelete)

		orderly := newGUID()
		Expect(vaultClient.Put(ctx, orderly+"/metadata", map[string]interface{}{
			"provision_requested_at": time.Now().Add(-8 * time.Hour).Format(time.RFC3339),
			"created_at":             time.Now().Add(-7 * time.Hour).Format(time.RFC3339),
			"delete_requested_at":    time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
		})).To(Succeed())
		fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(orderly))+"valkey_password")

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(Equal([]string{prefixFor(deploymentFor(orderly)) + "valkey_password"}))
	})

	It("does not prove an orphan whose provision_requested_at is not a time", func() {
		useMode(config.CredHubSweepDelete)

		garbled := newGUID()
		Expect(vaultClient.Put(ctx, garbled+"/metadata", map[string]interface{}{
			"delete_requested_at":    time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			"provision_requested_at": "last-tuesday",
		})).To(Succeed())
		fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(garbled))+"valkey_password")

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(BeEmpty())
		Expect(capture.output()).To(ContainSubstring("last-tuesday"))
	})

	It("proves an orphan whose delete request has the same time as its creation", func() {
		useMode(config.CredHubSweepDelete)

		sameSecond := newGUID()
		stamp := time.Now().Add(-3 * time.Hour).Format(time.RFC3339)
		Expect(vaultClient.Put(ctx, sameSecond+"/metadata", map[string]interface{}{
			"created_at":          stamp,
			"delete_requested_at": stamp,
		})).To(Succeed())
		fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(sameSecond))+"valkey_password")

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(Equal([]string{prefixFor(deploymentFor(sameSecond)) + "valkey_password"}))
	})

	It("does not prove an orphan whose created_at is not a time", func() {
		useMode(config.CredHubSweepDelete)

		garbled := newGUID()
		Expect(vaultClient.Put(ctx, garbled+"/metadata", map[string]interface{}{
			"delete_requested_at": time.Now().Add(-3 * time.Hour).Format(time.RFC3339),
			"created_at":          "yesterday-ish",
		})).To(Succeed())
		fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(garbled))+"valkey_password")

		sweepAndWait()

		Expect(fakeCredHub.Deleted()).To(BeEmpty())
		Expect(capture.output()).To(ContainSubstring("yesterday-ish"))
	})

	It("handles at most 10 proven candidates in one pass and defers the rest", func() {
		useMode(config.CredHubSweepDelete)

		cleaner := useBlockingCleaner(cleanerSucceeds)

		for range 25 {
			provenOrphan()
		}

		sweepAndWait()

		Expect(cleaner.Targets()).To(HaveLen(10))
		Expect(capture.output()).To(ContainSubstring("15 deferred"))
	})

	It("counts a proof the pass deadline cut short as deferred, not as examined or unproven", func() {
		useMode(config.CredHubSweepDelete)
		useBlockingCleaner(cleanerSucceeds)

		for range 3 {
			provenOrphan()
		}

		brokerInstance.SetCredentialSweepPassTimeout(100 * time.Millisecond)

		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			time.Sleep(300 * time.Millisecond)

			return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
		}

		sweepAndWait()

		Expect(capture.output()).To(ContainSubstring("examined 0 candidate deployments: 0 proven orphans handled, 0 unproven, 3 deferred to the next pass"))
	})

	It("examines the deployments the last pass could not prove after the rest", func() {
		useMode(config.CredHubSweepDelete)
		useBlockingCleaner(cleanerSucceeds)

		unprovenGUID, provenGUID := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
		seededIDs = append(seededIDs, unprovenGUID, provenGUID)

		requestDeprovision(provenGUID, 3*time.Hour)

		for _, guid := range []string{unprovenGUID, provenGUID} {
			fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(guid))+"valkey_password")
		}

		var (
			orderMu sync.Mutex
			order   []string
		)

		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			orderMu.Lock()
			order = append(order, name)
			orderMu.Unlock()

			return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
		}

		sweepAndWait()

		orderMu.Lock()
		Expect(order).To(Equal([]string{deploymentFor(unprovenGUID), deploymentFor(provenGUID)}))

		order = nil
		orderMu.Unlock()

		clock.Advance(2 * time.Hour)
		sweepAndWait()

		orderMu.Lock()
		defer orderMu.Unlock()

		Expect(order).To(Equal([]string{deploymentFor(provenGUID), deploymentFor(unprovenGUID)}))
	})

	It("keeps the same examination order across passes when a pass never reaches the unproven deployments", func() {
		useMode(config.CredHubSweepDelete)
		useBlockingCleaner(cleanerSucceeds)

		// Sorted by name, the unproven deployment comes before every proven one.
		unprovenGUID := "00000000-0000-4000-8000-000000000000"
		seededIDs = append(seededIDs, unprovenGUID)
		fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(unprovenGUID))+"valkey_password")

		for i := 1; i <= 11; i++ {
			guid := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
			seededIDs = append(seededIDs, guid)
			requestDeprovision(guid, 3*time.Hour)
			fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(guid))+"valkey_password")
		}

		var (
			orderMu sync.Mutex
			order   []string
		)

		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			orderMu.Lock()
			order = append(order, name)
			orderMu.Unlock()

			return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
		}

		takeOrder := func() []string {
			orderMu.Lock()
			defer orderMu.Unlock()

			taken := order
			order = nil

			return taken
		}

		sweepAndWait()

		first := takeOrder()
		Expect(first[0]).To(Equal(deploymentFor(unprovenGUID)))

		clock.Advance(2 * time.Hour)
		sweepAndWait()

		second := takeOrder()

		// The ten proven deployments use up the pass before the unproven one.
		Expect(second).To(HaveLen(10))
		Expect(second).NotTo(ContainElement(deploymentFor(unprovenGUID)))

		clock.Advance(2 * time.Hour)
		sweepAndWait()

		Expect(takeOrder()).To(Equal(second))
	})

	It("forgets a deployment that was unproven once a pass proves it", func() {
		useMode(config.CredHubSweepDelete)
		useBlockingCleaner(cleanerSucceeds)

		firstGUID, secondGUID := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"
		seededIDs = append(seededIDs, firstGUID, secondGUID)

		requestDeprovision(secondGUID, 3*time.Hour)

		for _, guid := range []string{firstGUID, secondGUID} {
			fakeCredHub.names = append(fakeCredHub.names, prefixFor(deploymentFor(guid))+"valkey_password")
		}

		var (
			orderMu sync.Mutex
			order   []string
		)

		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			orderMu.Lock()
			order = append(order, name)
			orderMu.Unlock()

			return nil, fmt.Errorf("%w: %s", bosh.ErrDeploymentNotFound, name)
		}

		sweepAndWait()

		// Cloud Foundry's request for the first deployment arrives, so it proves.
		requestDeprovision(firstGUID, 3*time.Hour)

		orderMu.Lock()
		order = nil
		orderMu.Unlock()

		clock.Advance(2 * time.Hour)
		sweepAndWait()

		clock.Advance(2 * time.Hour)
		orderMu.Lock()
		order = nil
		orderMu.Unlock()

		sweepAndWait()

		orderMu.Lock()
		defer orderMu.Unlock()

		Expect(order).To(Equal([]string{deploymentFor(firstGUID), deploymentFor(secondGUID)}))
	})

	It("returns at once while the cleaner blocks, and a second call during the pass does not list CredHub", func() {
		useMode(config.CredHubSweepDelete)
		provenOrphan()

		cleaner := useBlockingCleaner(cleanerBlocks)

		started := time.Now()

		brokerInstance.SweepOrphanedCredentials(ctx, live)
		Expect(time.Since(started)).To(BeNumerically("<", 50*time.Millisecond))

		Eventually(cleaner.Targets, 5*time.Second).Should(HaveLen(1))

		// Even with the hour gone, a pass still running blocks a new one.
		clock.Advance(2 * time.Hour)

		started = time.Now()

		brokerInstance.SweepOrphanedCredentials(ctx, live)
		Expect(time.Since(started)).To(BeNumerically("<", 50*time.Millisecond))
		Consistently(fakeCredHub.Finds, 300*time.Millisecond, 10*time.Millisecond).Should(HaveLen(1))
	})

	It("returns at once while CredHub's listing blocks", func() {
		useMode(config.CredHubSweepDelete)
		provenOrphan()

		fakeCredHub.mu.Lock()
		fakeCredHub.block = make(chan struct{})
		fakeCredHub.mu.Unlock()

		started := time.Now()

		brokerInstance.SweepOrphanedCredentials(ctx, live)
		Expect(time.Since(started)).To(BeNumerically("<", 50*time.Millisecond))
	})

	It("lists CredHub once for two calls within an hour, and again after the hour", func() {
		useMode(config.CredHubSweepDryRun)
		provenOrphan()

		sweepAndWait()
		clock.Advance(59 * time.Minute)
		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(HaveLen(1))

		clock.Advance(2 * time.Minute)
		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(HaveLen(2))
	})

	It("survives a cleaner panic and runs again after the hour", func() {
		useMode(config.CredHubSweepDelete)
		provenOrphan()
		useBlockingCleaner(cleanerPanics)

		sweepAndWait()

		Expect(capture.output()).To(ContainSubstring("panicked"))

		clock.Advance(61 * time.Minute)
		sweepAndWait()

		Expect(fakeCredHub.Finds()).To(HaveLen(2))
	})
})

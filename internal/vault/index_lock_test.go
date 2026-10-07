package vault_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/internal/vmmonitor/vmmonitortest"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// Every change to the index reads the whole "db" secret, changes one entry,
// and writes the whole secret back. Two callers in one process that overlap
// used to lose one of their changes.
var _ = Describe("Vault index read-modify-writes", func() {
	const (
		writers    = 24
		serviceID  = "valkey"
		planID     = "standalone"
		serviceKey = "service_id"
	)

	var (
		ctx    context.Context
		server *testutil.VaultDevServer
		client *vault.Vault
		prefix string
	)

	BeforeEach(func() {
		ctx = context.Background()

		var err error

		server, err = testutil.NewVaultDevServer(nil)
		Expect(err).ToNot(HaveOccurred())

		client = vault.New(server.Addr, server.RootToken, true)
		prefix = fmt.Sprintf("lock-%d", time.Now().UnixNano())
	})

	AfterEach(func() {
		server.Close()
	})

	key := func(i int) string {
		return fmt.Sprintf("%s-%02d", prefix, i)
	}

	// runAll starts one goroutine per index and returns the errors they
	// reported, once all of them have finished.
	runAll := func(count int, call func(i int) error) []error {
		var (
			waitGroup sync.WaitGroup
			errsMu    sync.Mutex
			errs      []error
		)

		record := func(index int, err error) {
			errsMu.Lock()
			defer errsMu.Unlock()

			errs = append(errs, fmt.Errorf("call %d: %w", index, err))
		}

		start := make(chan struct{})

		for index := range count {
			waitGroup.Add(1)

			go func() {
				defer waitGroup.Done()

				<-start

				err := call(index)
				if err != nil {
					record(index, err)
				}
			}()
		}

		close(start)
		waitGroup.Wait()

		return errs
	}

	It("keeps every entry that concurrent Index calls add", func() {
		errs := runAll(writers, func(i int) error {
			return client.Index(ctx, key(i), vaultPkg.Instance{
				ID: key(i), ServiceID: serviceID, PlanID: planID, DeploymentName: "standalone-" + key(i),
			})
		})
		Expect(errs).To(BeEmpty())

		idx, err := client.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())

		for i := range writers {
			Expect(idx.Data).To(HaveKey(key(i)))
		}

		Expect(idx.Data).To(HaveLen(writers))
	})

	It("keeps every change when removals, updates, and marks overlap", func() {
		for entry := range writers {
			Expect(client.Index(ctx, key(entry), map[string]interface{}{
				serviceKey: serviceID, "plan_id": planID, "deployment_name": "standalone-" + key(entry),
			})).To(Succeed())
		}

		const kinds = 5

		errs := runAll(writers, func(entry int) error {
			switch entry % kinds {
			case 0:
				return client.Index(ctx, key(entry), nil)
			case 1:
				return client.UpdateIndexEntry(ctx, key(entry), map[string]interface{}{"note": "updated"})
			case 2:
				return vmmonitortest.MarkDeleted(ctx, client, key(entry))
			case 3:
				client.Clear(key(entry))

				return nil
			default:
				return client.Index(ctx, key(entry)+"-new", map[string]interface{}{serviceKey: serviceID})
			}
		})
		Expect(errs).To(BeEmpty())

		idx, err := client.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())

		for entry := range writers {
			switch entry % kinds {
			case 0, 3:
				Expect(idx.Data).ToNot(HaveKey(key(entry)), "entry %s should have been removed", key(entry))
			case 1:
				Expect(idx.Data).To(HaveKey(key(entry)))
				Expect(idx.Data[key(entry)]).To(HaveKeyWithValue("note", "updated"))
				Expect(idx.Data[key(entry)]).To(HaveKeyWithValue(serviceKey, serviceID))
			case 2:
				Expect(idx.Data).To(HaveKey(key(entry)))
				Expect(idx.Data[key(entry)]).To(HaveKeyWithValue("status", "deleted"))
				Expect(idx.Data[key(entry)]).To(HaveKeyWithValue(serviceKey, serviceID))
			default:
				Expect(idx.Data).To(HaveKey(key(entry)))
				Expect(idx.Data).To(HaveKey(key(entry) + "-new"))
			}
		}
	})

	// expectLockReleasedAcrossUnseal has the first caller's index change find
	// Vault sealed, through the fault that seal injects, and holds the
	// auto-unseal that answer starts. A second caller then changes the index
	// while the first one waits, and both changes must survive the first
	// caller's retry.
	expectLockReleasedAcrossUnseal := func(seal func(proxy *testutil.VaultFaultProxy)) {
		proxy, err := testutil.NewVaultFaultProxy(server.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		client = vault.New(proxy.Addr, server.RootToken, true)

		apiClient, err := client.GetAPIClient()
		Expect(err).ToNot(HaveOccurred())
		apiClient.SetMaxRetries(0)

		unsealStarted := make(chan struct{})
		release := make(chan struct{})

		var startOnce, releaseOnce sync.Once

		defer releaseOnce.Do(func() { close(release) })

		client.SetAutoUnsealEnabled(true)
		client.SetAutoUnsealHook(func(context.Context) error {
			startOnce.Do(func() { close(unsealStarted) })
			<-release

			return nil
		})

		seal(proxy)

		var firstErr error

		firstDone := make(chan struct{})

		go func() {
			defer close(firstDone)

			firstErr = client.Index(ctx, key(0), map[string]interface{}{serviceKey: serviceID})
		}()

		Eventually(unsealStarted, 5*time.Second).Should(BeClosed())

		// A second caller changes the index while the first one waits.
		var secondErr error

		secondDone := make(chan struct{})

		go func() {
			defer close(secondDone)

			secondErr = client.Index(ctx, key(1), map[string]interface{}{serviceKey: serviceID})
		}()

		Eventually(secondDone, 5*time.Second).Should(BeClosed(), "the index lock was held across the auto-unseal wait")
		Expect(secondErr).ToNot(HaveOccurred())

		releaseOnce.Do(func() { close(release) })

		Eventually(firstDone, 5*time.Second).Should(BeClosed())
		Expect(firstErr).ToNot(HaveOccurred())

		// The first caller's retry read the index again, so the second
		// caller's entry survived it.
		idx, err := client.GetIndex(ctx, "db")
		Expect(err).ToNot(HaveOccurred())
		Expect(idx.Data).To(HaveKey(key(0)))
		Expect(idx.Data).To(HaveKey(key(1)), "the retry saved an index read before the unseal")
	}

	It("releases the index lock while it waits on an auto-unseal", func() {
		// The first caller's index read finds Vault sealed.
		expectLockReleasedAcrossUnseal(func(proxy *testutil.VaultFaultProxy) {
			proxy.FailReads("secret/data/db", http.StatusServiceUnavailable, "Vault is sealed", 1)
		})
	})

	It("reads the index again when its save finds Vault sealed", func() {
		// The first caller has read the index without the second caller's
		// entry, and its save finds Vault sealed. A retry that saved the
		// index it read before the unseal would drop that entry.
		expectLockReleasedAcrossUnseal(func(proxy *testutil.VaultFaultProxy) {
			proxy.FailWrites("secret/data/db", http.StatusServiceUnavailable, "Vault is sealed", 1)
		})
	})
})

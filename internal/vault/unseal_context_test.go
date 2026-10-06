package vault_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// A sealed Vault that stops answering must not hold a caller whose context
// has ended. The stub holds one request open until the client gives it up,
// and each spec ends the caller's context while that request is waiting.
var _ = Describe("Auto-unseal and the caller's context", func() {
	var (
		hold    string
		arrived chan string
		release chan struct{}
		stub    *httptest.Server
		client  *vault.Vault

		credentialsDir string
	)

	BeforeEach(func() {
		arrived = make(chan string, 8)
		release = make(chan struct{})

		stub = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			writer.Header().Set("Content-Type", "application/json")

			if req.URL.Path == hold {
				arrived <- req.URL.Path

				select {
				case <-req.Context().Done():
				case <-release:
				}

				return
			}

			switch req.URL.Path {
			case "/v1/sys/health", "/v1/sys/seal-status":
				_, _ = writer.Write([]byte(`{"sealed":true,"initialized":true,"t":1,"n":1}`))
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))

		var err error

		credentialsDir, err = os.MkdirTemp("", "unseal-context-")
		Expect(err).ToNot(HaveOccurred())

		credentials := filepath.Join(credentialsDir, "creds.json")
		Expect(os.WriteFile(credentials, []byte(`{"seal_key":"unseal-key","root_token":"root"}`), 0o600)).To(Succeed())

		client = vault.New(stub.URL, "root", true)
		client.SetCredentialsPath(credentials)
		client.SetAutoUnsealEnabled(true)
	})

	AfterEach(func() {
		close(release)
		stub.Close()

		Expect(os.RemoveAll(credentialsDir)).To(Succeed())
	})

	// returnsWhenTheCallerGivesUp runs the auto-unseal, ends the caller's
	// context once the held request has arrived, and expects the call to return.
	returnsWhenTheCallerGivesUp := func(heldPath string) {
		hold = heldPath

		callerCtx, endCaller := context.WithCancel(context.Background())
		defer endCaller()

		done := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			done <- client.AutoUnsealIfSealed(callerCtx)
		}()

		Eventually(arrived, 5*time.Second).Should(Receive(Equal(heldPath)))
		endCaller()

		var err error

		Eventually(done, 5*time.Second).Should(Receive(&err), "the call is still waiting on Vault after its caller's context ended")
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)
	}

	It("stops waiting for the health check when the caller's context ends", func() {
		returnsWhenTheCallerGivesUp("/v1/sys/health")
	})

	It("stops waiting for the unseal when the caller's context ends", func() {
		returnsWhenTheCallerGivesUp("/v1/sys/unseal")
	})
})

// sealedVaultStub answers the health and seal-status checks from a sealed
// flag and holds every unseal request until release is closed, after which it
// unseals. It records each unseal request, and each one its client cut off.
type sealedVaultStub struct {
	server         *httptest.Server
	sealed         atomic.Bool
	unsealRequests atomic.Int32
	arrived        chan struct{}
	sawCancel      chan struct{}
	release        chan struct{}
	releaseOnce    sync.Once
	credentialsDir string
}

func newSealedVaultStub() *sealedVaultStub {
	stub := &sealedVaultStub{
		arrived:   make(chan struct{}, 4),
		sawCancel: make(chan struct{}, 4),
		release:   make(chan struct{}),
	}
	stub.sealed.Store(true)

	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		switch req.URL.Path {
		case "/v1/sys/health", "/v1/sys/seal-status":
			_, _ = writer.Write([]byte(`{"sealed":` + boolJSON(stub.sealed.Load()) + `,"initialized":true,"t":1,"n":1}`))
		case "/v1/sys/unseal":
			// The server only notices a closed connection once the handler
			// has read the request body to its end.
			_, _ = io.Copy(io.Discard, req.Body)

			stub.unsealRequests.Add(1)
			stub.arrived <- struct{}{}

			select {
			case <-req.Context().Done():
				stub.sawCancel <- struct{}{}

				return
			case <-stub.release:
			}

			stub.sealed.Store(false)

			_, _ = writer.Write([]byte(`{"sealed":false,"initialized":true,"t":1,"n":1,"progress":0}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))

	var err error

	stub.credentialsDir, err = os.MkdirTemp("", "unseal-stub-")
	Expect(err).ToNot(HaveOccurred())

	return stub
}

// client returns a Vault client for the stub with auto-unseal on and a seal
// key on file.
func (stub *sealedVaultStub) client() *vault.Vault {
	credentials := filepath.Join(stub.credentialsDir, "creds.json")
	ExpectWithOffset(1, os.WriteFile(credentials, []byte(`{"seal_key":"unseal-key","root_token":"root"}`), 0o600)).To(Succeed())

	client := vault.New(stub.server.URL, "root", true)
	client.SetCredentialsPath(credentials)
	client.SetAutoUnsealEnabled(true)

	return client
}

func (stub *sealedVaultStub) releaseUnseal() {
	stub.releaseOnce.Do(func() { close(stub.release) })
}

func (stub *sealedVaultStub) close() {
	stub.releaseUnseal()
	stub.server.Close()

	Expect(os.RemoveAll(stub.credentialsDir)).To(Succeed())
}

// The unseal is a recovery action for every caller, and the cooldown is spent
// when it starts. A caller that gives up part way must not cut it off, or
// Vault stays sealed for the whole cooldown.
var _ = Describe("Auto-unseal that its caller abandons", func() {
	var (
		stub   *sealedVaultStub
		client *vault.Vault
	)

	BeforeEach(func() {
		stub = newSealedVaultStub()
		client = stub.client()
	})

	AfterEach(func() {
		stub.close()
	})

	It("still finishes the unseal when the caller's context ends during it", func() {
		callerCtx, endCaller := context.WithCancel(context.Background())
		defer endCaller()

		done := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			done <- client.AutoUnsealIfSealed(callerCtx)
		}()

		Eventually(stub.arrived, 5*time.Second).Should(Receive())
		endCaller()

		var err error

		Eventually(done, 5*time.Second).Should(Receive(&err), "the caller is still waiting after its context ended")
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)

		// A cut-off request shows up at the stub as a cancelled request
		// context, a moment after the caller's context ends.
		Consistently(stub.sawCancel, 500*time.Millisecond).ShouldNot(Receive(), "the unseal request was cancelled with its caller")
		Expect(stub.sealed.Load()).To(BeTrue(), "the stub unsealed before it was released")

		stub.releaseUnseal()

		Eventually(stub.sealed.Load, 5*time.Second).Should(BeFalse(), "the unseal did not finish after its caller gave up")

		// The next caller finds Vault unsealed, and the cooldown that the
		// first attempt spent has not cost anyone an unseal.
		Expect(client.AutoUnsealIfSealed(context.Background())).To(Succeed())
		Expect(stub.unsealRequests.Load()).To(Equal(int32(1)))
	})
})

// Callers that find Vault sealed while an unseal is already running share that
// unseal. They wait for its result, and none of them starts a second one.
var _ = Describe("Auto-unseal shared by concurrent callers", func() {
	var (
		stub   *sealedVaultStub
		client *vault.Vault
	)

	BeforeEach(func() {
		stub = newSealedVaultStub()
		client = stub.client()
	})

	AfterEach(func() {
		stub.close()
	})

	// sealedUntilUnsealed is an operation that fails as a sealed Vault does
	// until the stub has been unsealed.
	sealedUntilUnsealed := func() error {
		if stub.sealed.Load() {
			return vaultPkg.ErrIsSealed
		}

		return nil
	}

	It("lets a caller that arrives during the unseal wait for it and succeed", func() {
		first := make(chan error, 1)
		second := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			first <- client.WithAutoUnseal(context.Background(), sealedUntilUnsealed)
		}()

		Eventually(stub.arrived, 5*time.Second).Should(Receive())

		go func() {
			defer GinkgoRecover()

			second <- client.WithAutoUnseal(context.Background(), sealedUntilUnsealed)
		}()

		// The second caller waits for the unseal in flight instead of
		// retrying into the sealed Vault.
		Consistently(second, 500*time.Millisecond).ShouldNot(Receive(), "the second caller did not wait for the unseal in flight")

		stub.releaseUnseal()

		Eventually(first, 5*time.Second).Should(Receive(BeNil()))
		Eventually(second, 5*time.Second).Should(Receive(BeNil()), "the second caller did not share the unseal's success")
		Expect(stub.unsealRequests.Load()).To(Equal(int32(1)), "the unseal ran more than once")
	})

	It("returns the context error to a waiting caller whose context ends first, and still finishes the unseal", func() {
		first := make(chan error, 1)
		second := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			first <- client.AutoUnsealIfSealed(context.Background())
		}()

		Eventually(stub.arrived, 5*time.Second).Should(Receive())

		waiterCtx, endWaiter := context.WithCancel(context.Background())
		defer endWaiter()

		go func() {
			defer GinkgoRecover()

			second <- client.AutoUnsealIfSealed(waiterCtx)
		}()

		Consistently(second, 500*time.Millisecond).ShouldNot(Receive(), "the waiting caller returned before the unseal finished")

		endWaiter()

		var err error

		Eventually(second, 5*time.Second).Should(Receive(&err), "the waiting caller did not stop when its context ended")
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)
		Expect(stub.sealed.Load()).To(BeTrue(), "the stub unsealed before it was released")

		stub.releaseUnseal()

		Eventually(first, 5*time.Second).Should(Receive(BeNil()), "the unseal did not finish for the caller that started it")
		Expect(stub.sealed.Load()).To(BeFalse())
		Expect(stub.unsealRequests.Load()).To(Equal(int32(1)), "the unseal ran more than once")
	})
})

// unsealStartPanics is a logger whose first "attempting auto-unseal" message,
// which the unseal logs before it sends the key, waits for release and then
// panics. Every Error message is recorded.
type unsealStartPanics struct {
	*logger.NoOpLogger

	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}

	mu     sync.Mutex
	errors []string
}

const unsealStartMessage = "vault is sealed; attempting auto-unseal"

func (l *unsealStartPanics) Info(msg string, _ ...interface{}) {
	if msg == unsealStartMessage && l.armed.CompareAndSwap(true, false) {
		l.entered <- struct{}{}

		<-l.release

		panic("injected unseal panic")
	}
}

func (l *unsealStartPanics) Error(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errors = append(l.errors, fmt.Sprintf(msg, args...))
}

func (l *unsealStartPanics) Named(string) logger.Logger { return l }

func (l *unsealStartPanics) loggedErrors() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.errors, "\n")
}

// A panic in the unseal must not take the process down, and must not leave the
// callers waiting on that unseal hung.
var _ = Describe("Auto-unseal that panics", func() {
	var (
		stub     *sealedVaultStub
		client   *vault.Vault
		panicky  *unsealStartPanics
		previous logger.Logger
	)

	BeforeEach(func() {
		stub = newSealedVaultStub()
		client = stub.client()

		panicky = &unsealStartPanics{
			NoOpLogger: &logger.NoOpLogger{},
			entered:    make(chan struct{}, 1),
			release:    make(chan struct{}),
		}
		panicky.armed.Store(true)

		previous = logger.Get()

		logger.Set(panicky)
	})

	AfterEach(func() {
		logger.Set(previous)
		stub.close()
	})

	It("returns the panic as an error to every waiting caller, logs its stack, and clears the unseal in flight", func() {
		first := make(chan error, 1)
		second := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			first <- client.AutoUnsealIfSealed(context.Background())
		}()

		Eventually(panicky.entered, 5*time.Second).Should(Receive())

		go func() {
			defer GinkgoRecover()

			second <- client.AutoUnsealIfSealed(context.Background())
		}()

		Consistently(second, 300*time.Millisecond).ShouldNot(Receive(), "the second caller did not wait for the unseal in flight")

		close(panicky.release)

		for _, results := range []chan error{first, second} {
			var err error

			Eventually(results, 5*time.Second).Should(Receive(&err), "a caller is still waiting on the unseal that panicked")
			Expect(errors.Is(err, vault.ErrUnsealPanicked)).To(BeTrue(), "the error does not report the panic: %v", err)
			Expect(err.Error()).To(ContainSubstring("injected unseal panic"))
		}

		Expect(panicky.loggedErrors()).To(ContainSubstring("auto-unseal panicked: injected unseal panic"))
		Expect(panicky.loggedErrors()).To(ContainSubstring("performUnseal"), "the log does not carry the panic's stack")

		// A later caller is held off by the cooldown and does not join the
		// finished call, so the record of the unseal in flight was cleared.
		Expect(client.AutoUnsealIfSealed(context.Background())).To(Succeed())
		Expect(stub.unsealRequests.Load()).To(BeZero())
	})
})

func boolJSON(value bool) string {
	if value {
		return "true"
	}

	return "false"
}

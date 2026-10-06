package vault_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"blacksmith/internal/vault"
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

// The unseal is a recovery action for every caller, and the cooldown is spent
// when it starts. A caller that gives up part way must not cut it off, or
// Vault stays sealed for the whole cooldown.
var _ = Describe("Auto-unseal that its caller abandons", func() {
	var (
		sealed         atomic.Bool
		unsealRequests atomic.Int32
		arrived        chan struct{}
		sawCancel      chan struct{}
		release        chan struct{}
		stub           *httptest.Server
		client         *vault.Vault

		credentialsDir string
	)

	BeforeEach(func() {
		sealed.Store(true)
		unsealRequests.Store(0)

		arrived = make(chan struct{}, 4)
		sawCancel = make(chan struct{}, 4)
		release = make(chan struct{})

		stub = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			writer.Header().Set("Content-Type", "application/json")

			switch req.URL.Path {
			case "/v1/sys/health", "/v1/sys/seal-status":
				_, _ = writer.Write([]byte(`{"sealed":` + boolJSON(sealed.Load()) + `,"initialized":true,"t":1,"n":1}`))
			case "/v1/sys/unseal":
				// The server only notices a closed connection once the handler
				// has read the request body to its end.
				_, _ = io.Copy(io.Discard, req.Body)

				unsealRequests.Add(1)
				arrived <- struct{}{}

				select {
				case <-req.Context().Done():
					sawCancel <- struct{}{}

					return
				case <-release:
				}

				sealed.Store(false)

				_, _ = writer.Write([]byte(`{"sealed":false,"initialized":true,"t":1,"n":1,"progress":0}`))
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))

		var err error

		credentialsDir, err = os.MkdirTemp("", "unseal-abandoned-")
		Expect(err).ToNot(HaveOccurred())

		credentials := filepath.Join(credentialsDir, "creds.json")
		Expect(os.WriteFile(credentials, []byte(`{"seal_key":"unseal-key","root_token":"root"}`), 0o600)).To(Succeed())

		client = vault.New(stub.URL, "root", true)
		client.SetCredentialsPath(credentials)
		client.SetAutoUnsealEnabled(true)
	})

	AfterEach(func() {
		select {
		case <-release:
		default:
			close(release)
		}

		stub.Close()

		Expect(os.RemoveAll(credentialsDir)).To(Succeed())
	})

	It("still finishes the unseal when the caller's context ends during it", func() {
		callerCtx, endCaller := context.WithCancel(context.Background())
		defer endCaller()

		done := make(chan error, 1)

		go func() {
			defer GinkgoRecover()

			done <- client.AutoUnsealIfSealed(callerCtx)
		}()

		Eventually(arrived, 5*time.Second).Should(Receive())
		endCaller()

		var err error

		Eventually(done, 5*time.Second).Should(Receive(&err), "the caller is still waiting after its context ended")
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "the error does not carry the context's: %v", err)

		// A cut-off request shows up at the stub as a cancelled request
		// context, a moment after the caller's context ends.
		Consistently(sawCancel, 500*time.Millisecond).ShouldNot(Receive(), "the unseal request was cancelled with its caller")
		Expect(sealed.Load()).To(BeTrue(), "the stub unsealed before it was released")

		close(release)

		Eventually(sealed.Load, 5*time.Second).Should(BeFalse(), "the unseal did not finish after its caller gave up")

		// The next caller finds Vault unsealed, and the cooldown that the
		// first attempt spent has not cost anyone an unseal.
		Expect(client.AutoUnsealIfSealed(context.Background())).To(Succeed())
		Expect(unsealRequests.Load()).To(Equal(int32(1)))
	})
})

func boolJSON(value bool) string {
	if value {
		return "true"
	}

	return "false"
}

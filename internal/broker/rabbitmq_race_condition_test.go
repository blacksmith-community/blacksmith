package broker_test

import (
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("RabbitMQ Race Condition Prevention", func() {
	var (
		brokerInstance  *broker.Broker
		mockServer      *httptest.Server
		deleteCount     atomic.Int32
		createCount     atomic.Int32
		serverResponses map[string]func(w http.ResponseWriter, r *http.Request)
	)

	BeforeEach(func() {
		deleteCount.Store(0)
		createCount.Store(0)
		serverResponses = make(map[string]func(w http.ResponseWriter, r *http.Request))

		// Create a mock RabbitMQ management API server
		mockServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if handler, exists := serverResponses[request.URL.Path]; exists {
				handler(writer, request)

				return
			}

			// Default handlers for RabbitMQ API endpoints
			switch {
			case strings.HasPrefix(request.URL.Path, "/api/users/") && request.Method == http.MethodDelete:
				count := deleteCount.Add(1)

				// Simulate the race condition scenario:
				// First delete succeeds, subsequent ones get 404
				if count == 1 {
					writer.WriteHeader(http.StatusNoContent)
				} else {
					writer.WriteHeader(http.StatusNotFound)
					_, _ = writer.Write([]byte(`{"error":"Object Not Found","reason":"Not Found"}`))
				}

			case strings.HasPrefix(request.URL.Path, "/api/users/") && request.Method == http.MethodPut:
				count := createCount.Add(1)

				// Simulate user already exists on second attempt
				if count == 1 {
					writer.WriteHeader(http.StatusCreated)
				} else {
					writer.WriteHeader(http.StatusConflict)
					_, _ = writer.Write([]byte(`{"error":"Conflict","reason":"User already exists"}`))
				}

			case strings.HasPrefix(request.URL.Path, "/api/permissions/"):
				writer.WriteHeader(http.StatusCreated)

			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))

		// Initialize broker with mock configuration
		brokerInstance = &broker.Broker{
			Config:        &config.Config{},
			InstanceLocks: make(map[string]*sync.Mutex),
		}
	})

	AfterEach(func() {
		mockServer.Close()
	})

	Describe("Idempotent Delete Operations", func() {
		It("should treat 404 responses as successful deletion", func() {
			ctx := context.Background()

			// Provide connection info that tryServiceRequest expects
			// Parse URL to extract just the hostname without scheme
			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			// First delete - should succeed with 204
			err := broker.DeletetUserRabbitMQ(ctx,
				"test-binding-1",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred())

			// Second delete - should get 404 but still succeed
			err = broker.DeletetUserRabbitMQ(ctx,
				"test-binding-2",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred()) // Should succeed despite 404
			Expect(deleteCount.Load()).To(Equal(int32(2)))
		})

		It("should fail on non-404 errors", func() {
			// Override response to return 500 error
			serverResponses["/api/users/test-binding-error"] = func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusInternalServerError)
				_, _ = writer.Write([]byte(`{"error":"Internal Server Error"}`))
			}

			ctx := context.Background()
			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}
			err := broker.DeletetUserRabbitMQ(ctx,
				"test-binding-error",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("500"))
		})
	})

	Describe("Idempotent Create Operations", func() {
		It("should treat 409 Conflict as successful creation", func() {
			ctx := context.Background()

			// Provide connection info that tryServiceRequest expects
			// Parse URL to extract just the hostname without scheme
			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			// First create - should succeed with 201
			err := broker.CreateUserPassRabbitMQ(ctx,
				"test-user-1",
				"test-pass",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred())

			// Second create - should get 409 but still succeed
			err = broker.CreateUserPassRabbitMQ(ctx,
				"test-user-2",
				"test-pass",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred()) // Should succeed despite 409
			Expect(createCount.Load()).To(Equal(int32(2)))
		})

		It("should accept 204 No Content as success", func() {
			serverResponses["/api/users/test-user-204"] = func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}

			ctx := context.Background()
			creds := map[string]interface{}{
				"hostname": strings.TrimPrefix(mockServer.URL, "http://"),
				"ips":      []string{"127.0.0.1"},
			}
			err := broker.CreateUserPassRabbitMQ(ctx,
				"test-user-204",
				"test-pass",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("Concurrent Instance Locking", func() {
		It("should serialize operations on the same instance", func() {
			const numOperations = 10
			const instanceID = "test-instance-concurrent"

			var waitGroup sync.WaitGroup
			// Track when each operation holds the lock
			lockAcquired := make([]time.Time, numOperations)
			lockReleased := make([]time.Time, numOperations)

			// Track execution order
			var executionOrder []int
			var orderMutex sync.Mutex

			for operationIndex := range numOperations {
				waitGroup.Add(1)
				go func(index int) {
					defer waitGroup.Done()

					// Simulate using the instance lock
					mutex := brokerInstance.GetInstanceLock(instanceID)
					mutex.Lock()
					lockAcquired[index] = time.Now()

					// Track execution order
					orderMutex.Lock()
					executionOrder = append(executionOrder, index)
					orderMutex.Unlock()

					// Simulate work
					time.Sleep(10 * time.Millisecond)

					lockReleased[index] = time.Now()
					mutex.Unlock()
				}(operationIndex)
			}

			waitGroup.Wait()

			// Verify no two operations held lock simultaneously
			for opIndex := range numOperations {
				for compareIndex := range numOperations {
					if opIndex != compareIndex {
						// Check if operation opIndex held the lock while operation compareIndex also held it
						if lockAcquired[opIndex].Before(lockReleased[compareIndex]) && lockAcquired[compareIndex].Before(lockReleased[opIndex]) {
							// They held the lock at the same time - this should not happen
							Fail(fmt.Sprintf("Operations %d and %d held lock simultaneously", opIndex, compareIndex))
						}
					}
				}
			}

			// Verify all operations completed
			Expect(executionOrder).To(HaveLen(numOperations))
		})

		It("should handle mixed bind and unbind operations without race conditions", func() {
			const numOperations = 20
			const instanceID = "test-instance-mixed"
			var waitGroup sync.WaitGroup
			var operationCounter atomic.Int32

			for operationIndex := range numOperations {
				waitGroup.Add(1)
				go func(_ int) {
					defer waitGroup.Done()

					// Acquire lock for this instance
					mutex := brokerInstance.GetInstanceLock(instanceID)
					mutex.Lock()
					defer mutex.Unlock()

					// Simulate operation
					operationCounter.Add(1)
					time.Sleep(5 * time.Millisecond)
				}(operationIndex)
			}

			waitGroup.Wait()

			// Verify all operations completed
			Expect(operationCounter.Load()).To(Equal(int32(numOperations)))
		})
	})

	Describe("Real-world Race Condition Scenario", func() {
		It("should handle the UCD deployment scenario without deadlock", func() {
			const numBindings = 5
			const instanceID = "rabbitmq-instance-001"

			// Track state
			var userExists sync.Map

			// Override server to simulate more realistic behavior
			mockServer.Close() // Close the default server
			mockServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasPrefix(request.URL.Path, "/api/users/") {
					parts := strings.Split(request.URL.Path, "/")
					userID := parts[len(parts)-1]

					switch request.Method {
					case http.MethodDelete:
						if _, exists := userExists.Load(userID); exists {
							userExists.Delete(userID)
							writer.WriteHeader(http.StatusNoContent)
						} else {
							writer.WriteHeader(http.StatusNotFound)
							_, _ = writer.Write([]byte(`{"error":"Object Not Found","reason":"Not Found"}`))
						}

					case http.MethodPut:
						if _, exists := userExists.Load(userID); exists {
							writer.WriteHeader(http.StatusConflict)
						} else {
							userExists.Store(userID, true)
							writer.WriteHeader(http.StatusCreated)
						}
					}
				} else {
					writer.WriteHeader(http.StatusNotFound)
				}
			}))

			// Create initial bindings
			for bindingIndex := range numBindings {
				bindingID := fmt.Sprintf("app-binding-%d", bindingIndex)
				userExists.Store(bindingID, true)
			}

			// Simulate app deletion triggering concurrent unbind operations
			var waitGroup sync.WaitGroup
			unbindErrors := make([]error, numBindings)

			for bindingIndex := range numBindings {
				waitGroup.Add(1)
				go func(index int) {
					defer waitGroup.Done()

					bindingID := fmt.Sprintf("app-binding-%d", index)

					// Simulate unbind with proper locking
					mutex := brokerInstance.GetInstanceLock(instanceID)
					mutex.Lock()
					defer mutex.Unlock()

					// Delete the user
					ctx := context.Background()
					parsedURL, _ := url.Parse(mockServer.URL)
					creds := map[string]interface{}{
						"hostname": parsedURL.Host,
						"ips":      []string{"127.0.0.1"},
					}
					err := broker.DeletetUserRabbitMQ(ctx,
						bindingID,
						"admin",
						"admin-pass",
						mockServer.URL+"/api",
						brokerInstance.Config,
						creds)

					unbindErrors[index] = err
				}(bindingIndex)
			}

			waitGroup.Wait()

			// Verify all unbind operations succeeded (even with 404s)
			for i, err := range unbindErrors {
				Expect(err).ToNot(HaveOccurred(),
					fmt.Sprintf("Unbind %d should succeed even if user already deleted", i))
			}

			// Verify we can create new bindings after the chaos
			for i := range 3 {
				bindingID := fmt.Sprintf("new-binding-%d", i)

				instanceMutex := brokerInstance.GetInstanceLock(instanceID)
				instanceMutex.Lock()

				ctx := context.Background()
				creds := map[string]interface{}{
					"hostname": strings.TrimPrefix(mockServer.URL, "http://"),
					"ips":      []string{"127.0.0.1"},
				}
				err := broker.CreateUserPassRabbitMQ(ctx,
					bindingID,
					"new-password",
					"admin",
					"admin-pass",
					mockServer.URL+"/api",
					brokerInstance.Config,
					creds)

				instanceMutex.Unlock()

				Expect(err).ToNot(HaveOccurred(),
					"Should be able to create new binding "+bindingID)
			}
		})
	})
})

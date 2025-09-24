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

type requestRecord struct {
	Timestamp time.Time
	Method    string
	Path      string
	Attempt   int
}

var _ = Describe("RabbitMQ Retry Mechanism", func() {
	var (
		brokerInstance  *broker.Broker
		mockServer      *httptest.Server
		requestLog      []requestRecord
		requestMutex    sync.Mutex
		failureCounter  map[string]*atomic.Int32
		serverResponses map[string]func(w http.ResponseWriter, r *http.Request)
	)

	BeforeEach(func() {
		requestLog = []requestRecord{}
		failureCounter = make(map[string]*atomic.Int32)
		serverResponses = make(map[string]func(w http.ResponseWriter, r *http.Request))

		// Create mock RabbitMQ server with request logging
		mockServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			// Log the request
			requestMutex.Lock()
			requestLog = append(requestLog, requestRecord{
				Timestamp: time.Now(),
				Method:    request.Method,
				Path:      request.URL.Path,
				Attempt:   len(requestLog) + 1,
			})
			requestMutex.Unlock()

			// Check for custom handler
			if handler, exists := serverResponses[request.URL.Path]; exists {
				handler(writer, request)

				return
			}

			// Default behavior
			writer.WriteHeader(http.StatusOK)
		}))

		// Initialize broker with minimal configuration
		brokerInstance = &broker.Broker{
			Config: &config.Config{
				Debug: false,
			},
			InstanceLocks: make(map[string]*sync.Mutex),
		}
	})

	AfterEach(func() {
		mockServer.Close()
	})

	Describe("Exponential Backoff", func() {
		It("should implement exponential backoff between retries", func() {
			ctx := context.Background()
			attemptCount := atomic.Int32{}

			// Set up server to fail 2 times, then succeed
			serverResponses["/api/users/backoff-test"] = func(writer http.ResponseWriter, _ *http.Request) {
				count := attemptCount.Add(1)
				if count <= 2 {
					writer.WriteHeader(http.StatusServiceUnavailable)
					_, _ = writer.Write([]byte(`{"error":"Service temporarily unavailable"}`))
				} else {
					writer.WriteHeader(http.StatusCreated)
				}
			}

			// Provide connection info
			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			// Execute operation (should retry with exponential backoff)
			startTime := time.Now()
			err := broker.CreateUserPassRabbitMQ(ctx,
				"backoff-test",
				"password",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			duration := time.Since(startTime)
			Expect(err).ToNot(HaveOccurred())
			Expect(attemptCount.Load()).To(Equal(int32(3)))

			// Verify backoff timing
			// With 50ms base delay: attempt 1, wait 50ms, attempt 2, wait 100ms, attempt 3
			// Total minimum time should be ~150ms
			Expect(duration).To(BeNumerically(">=", 100*time.Millisecond))

			// Check request timing from log
			requestMutex.Lock()
			defer requestMutex.Unlock()
			Expect(requestLog).To(HaveLen(3))

			// Verify increasing delays between attempts
			if len(requestLog) >= 3 {
				delay1 := requestLog[1].Timestamp.Sub(requestLog[0].Timestamp)
				delay2 := requestLog[2].Timestamp.Sub(requestLog[1].Timestamp)

				// Second delay should be roughly double the first
				Expect(delay2).To(BeNumerically(">", delay1))
			}
		})
	})

	Describe("Retry Limits", func() {
		It("should respect maximum retry limit", func() {
			ctx := context.Background()
			attemptCount := atomic.Int32{}

			// Server always returns error
			serverResponses["/api/users/max-retry-test"] = func(writer http.ResponseWriter, _ *http.Request) {
				attemptCount.Add(1)
				writer.WriteHeader(http.StatusServiceUnavailable)
				_, _ = writer.Write([]byte(`{"error":"Service unavailable"}`))
			}

			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			// Should fail after max retries
			err := broker.CreateUserPassRabbitMQ(ctx,
				"max-retry-test",
				"password",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("503"))
			// Should attempt initial + 3 retries = 4 total
			Expect(attemptCount.Load()).To(Equal(int32(4)))
		})

		It("should succeed immediately if first attempt works", func() {
			ctx := context.Background()
			attemptCount := atomic.Int32{}

			// Server succeeds on first try
			serverResponses["/api/users/immediate-success"] = func(writer http.ResponseWriter, _ *http.Request) {
				attemptCount.Add(1)
				writer.WriteHeader(http.StatusCreated)
			}

			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			err := broker.CreateUserPassRabbitMQ(ctx,
				"immediate-success",
				"password",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred())
			Expect(attemptCount.Load()).To(Equal(int32(1))) // No retries needed
		})
	})

	Describe("Context Cancellation", func() {
		It("should stop retrying when context is cancelled", func() {
			attemptCount := atomic.Int32{}

			// Server always fails
			serverResponses["/api/users/context-cancel-test"] = func(writer http.ResponseWriter, _ *http.Request) {
				attemptCount.Add(1)
				time.Sleep(100 * time.Millisecond) // Slow response
				writer.WriteHeader(http.StatusServiceUnavailable)
			}

			// Create context with timeout
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()

			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			err := broker.CreateUserPassRabbitMQ(ctx,
				"context-cancel-test",
				"password",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).To(HaveOccurred())
			// Should have cancelled before all retries
			Expect(attemptCount.Load()).To(BeNumerically("<=", int32(2)))
		})
	})

	Describe("Delete Operation Retries", func() {
		It("should retry delete operations on transient failures", func() {
			ctx := context.Background()
			attemptCount := atomic.Int32{}

			// Fail twice, then succeed
			serverResponses["/api/users/delete-retry-test"] = func(writer http.ResponseWriter, _ *http.Request) {
				count := attemptCount.Add(1)
				if count <= 2 {
					writer.WriteHeader(http.StatusGatewayTimeout)
				} else {
					writer.WriteHeader(http.StatusNoContent)
				}
			}

			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			err := broker.DeletetUserRabbitMQ(ctx,
				"delete-retry-test",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred())
			Expect(attemptCount.Load()).To(Equal(int32(3)))
		})

		It("should not retry on 404 (user not found)", func() {
			ctx := context.Background()
			attemptCount := atomic.Int32{}

			// Return 404 immediately
			serverResponses["/api/users/delete-404-test"] = func(writer http.ResponseWriter, _ *http.Request) {
				attemptCount.Add(1)
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte(`{"error":"Not Found"}`))
			}

			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			err := broker.DeletetUserRabbitMQ(ctx,
				"delete-404-test",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			Expect(err).ToNot(HaveOccurred())               // 404 is treated as success
			Expect(attemptCount.Load()).To(Equal(int32(1))) // No retries for 404
		})
	})

	Describe("Selective Retry Logic", func() {
		It("should only retry on retryable errors", func() {
			testCases := []struct {
				name          string
				statusCode    int
				shouldRetry   bool
				expectedError bool
			}{
				{"BadRequest", http.StatusBadRequest, false, true},
				{"Unauthorized", http.StatusUnauthorized, false, true},
				{"Conflict", http.StatusConflict, false, false}, // Idempotent
				{"InternalError", http.StatusInternalServerError, true, true},
				{"BadGateway", http.StatusBadGateway, true, true},
				{"ServiceUnavailable", http.StatusServiceUnavailable, true, true},
				{"GatewayTimeout", http.StatusGatewayTimeout, true, true},
			}

			for _, testCase := range testCases {
				func(testData struct {
					name          string
					statusCode    int
					shouldRetry   bool
					expectedError bool
				}) {
					ctx := context.Background()
					attemptCount := atomic.Int32{}
					path := "/api/users/" + testData.name

					serverResponses[path] = func(writer http.ResponseWriter, _ *http.Request) {
						attemptCount.Add(1)
						writer.WriteHeader(testData.statusCode)
						_, _ = fmt.Fprintf(writer, `{"error":"%s"}`, testData.name)
					}

					creds := map[string]interface{}{
						"hostname": strings.TrimPrefix(mockServer.URL, "http://"),
						"ips":      []string{"127.0.0.1"},
					}

					err := broker.CreateUserPassRabbitMQ(ctx,
						testData.name,
						"password",
						"admin",
						"admin-pass",
						mockServer.URL+"/api",
						brokerInstance.Config,
						creds)

					if testData.expectedError {
						Expect(err).To(HaveOccurred())
					} else {
						Expect(err).ToNot(HaveOccurred())
					}

					if testData.shouldRetry {
						// Should have retried (4 attempts total)
						Expect(attemptCount.Load()).To(Equal(int32(4)))
					} else {
						// Should not have retried
						Expect(attemptCount.Load()).To(Equal(int32(1)))
					}
				}(testCase)
			}
		})
	})

	Describe("Concurrent Retry Operations", func() {
		It("should handle multiple operations retrying simultaneously", func() {
			ctx := context.Background()
			const numOperations = 5
			var waitGroup sync.WaitGroup
			errors := make([]error, numOperations)

			// Each operation will need 2 retries to succeed
			for i := range numOperations {
				userID := fmt.Sprintf("concurrent-retry-%d", i)
				counter := &atomic.Int32{}
				failureCounter[userID] = counter

				serverResponses["/api/users/"+userID] = func(writer http.ResponseWriter, _ *http.Request) {
					count := counter.Add(1)
					if count <= 2 {
						writer.WriteHeader(http.StatusServiceUnavailable)
					} else {
						writer.WriteHeader(http.StatusCreated)
					}
				}
			}

			// Execute operations concurrently
			for operationIdx := range numOperations {
				waitGroup.Add(1)
				go func(idx int) {
					defer waitGroup.Done()
					userID := fmt.Sprintf("concurrent-retry-%d", idx)

					creds := map[string]interface{}{
						"hostname": strings.TrimPrefix(mockServer.URL, "http://"),
						"ips":      []string{"127.0.0.1"},
					}

					errors[idx] = broker.CreateUserPassRabbitMQ(ctx,
						userID,
						"password",
						"admin",
						"admin-pass",
						mockServer.URL+"/api",
						brokerInstance.Config,
						creds)
				}(operationIdx)
			}

			waitGroup.Wait()

			// Verify all succeeded after retries
			for i, err := range errors {
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Operation %d should succeed", i))
			}

			// Verify each operation retried correctly
			for i := range numOperations {
				userID := fmt.Sprintf("concurrent-retry-%d", i)
				Expect(failureCounter[userID].Load()).To(Equal(int32(3)),
					fmt.Sprintf("User %s should have been attempted 3 times", userID))
			}
		})
	})

	Describe("Retry with Different Error Types", func() {
		It("should handle network errors with retry", func() {
			ctx := context.Background()
			attemptCount := atomic.Int32{}

			// Close server to simulate network error
			mockServer.Close()

			// Create a new server that tracks attempts
			mockServer = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				count := attemptCount.Add(1)
				if count == 1 {
					// First attempt: close connection to simulate network error
					hj, ok := writer.(http.Hijacker)
					if ok {
						conn, _, _ := hj.Hijack()
						_ = conn.Close()
					}
				} else {
					// Subsequent attempts succeed
					writer.WriteHeader(http.StatusCreated)
				}
			}))
			defer mockServer.Close()

			parsedURL, _ := url.Parse(mockServer.URL)
			creds := map[string]interface{}{
				"hostname": parsedURL.Host,
				"ips":      []string{"127.0.0.1"},
			}

			err := broker.CreateUserPassRabbitMQ(ctx,
				"network-error-test",
				"password",
				"admin",
				"admin-pass",
				mockServer.URL+"/api",
				brokerInstance.Config,
				creds)

			// Should eventually succeed after network error
			Expect(err).ToNot(HaveOccurred())
			Expect(attemptCount.Load()).To(BeNumerically(">", int32(1)))
		})
	})
})

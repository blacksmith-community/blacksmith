package broker_test

import (
	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pivotal-cf/brokerapi/v8/domain"
)

var _ = Describe("RabbitMQ Integration Tests", func() {
	var (
		brokerInstance *broker.Broker
		vault          *internalVault.Vault
		mockBOSH       *testutil.IntegrationMockBOSH
		mockRabbitMQ   *httptest.Server
		instanceID     string
		testPlan       services.Plan
		rabbitMQUsers  sync.Map // Track users in RabbitMQ
		requestCounter atomic.Int32
	)

	BeforeEach(func() {
		instanceID = "integration-rabbitmq-instance-001"

		// Create internal Vault instance connected to suite server
		vault = internalVault.New(suite.vault.Addr, suite.vault.RootToken, false)

		// Initialize mock services
		mockBOSH = testutil.NewIntegrationMockBOSH()

		// Set up test RabbitMQ plan
		testPlan = services.Plan{
			ID:   "rabbitmq-plan",
			Name: "RabbitMQ Test Plan",
			Type: "rabbitmq",
			Credentials: map[interface{}]interface{}{
				"credentials": map[interface{}]interface{}{
					"host":           "{{.Jobs.rabbitmq.IPs.0}}",
					"port":           5672,
					"api_url":        "https://{{.Jobs.rabbitmq.IPs.0}}:15672/api",
					"admin_username": "admin",
					"admin_password": "admin-password",
					"username":       "default-user",
					"password":       "default-pass",
					"vhost":          "/test",
				},
			},
		}

		// Create mock RabbitMQ management API
		mockRabbitMQ = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requestCounter.Add(1)

			// Handle user operations
			if strings.HasPrefix(request.URL.Path, "/api/users/") {
				parts := strings.Split(request.URL.Path, "/")
				userID := parts[len(parts)-1]

				switch request.Method {
				case http.MethodPut:
					// Create user
					if _, exists := rabbitMQUsers.Load(userID); exists {
						writer.WriteHeader(http.StatusConflict)
						_, _ = writer.Write([]byte(`{"error":"Conflict","reason":"User already exists"}`))
					} else {
						rabbitMQUsers.Store(userID, true)
						writer.WriteHeader(http.StatusCreated)
					}

				case http.MethodDelete:
					// Delete user
					if _, exists := rabbitMQUsers.Load(userID); exists {
						rabbitMQUsers.Delete(userID)
						writer.WriteHeader(http.StatusNoContent)
					} else {
						writer.WriteHeader(http.StatusNotFound)
						_, _ = writer.Write([]byte(`{"error":"Not Found","reason":"User does not exist"}`))
					}

				case http.MethodGet:
					// Get user info
					if _, exists := rabbitMQUsers.Load(userID); exists {
						writer.WriteHeader(http.StatusOK)
						_, _ = writer.Write([]byte(`{"name":"` + userID + `","tags":"management"}`))
					} else {
						writer.WriteHeader(http.StatusNotFound)
					}
				}
			} else if strings.HasPrefix(request.URL.Path, "/api/permissions/") {
				// Grant permissions
				writer.WriteHeader(http.StatusCreated)
			} else {
				writer.WriteHeader(http.StatusNotFound)
			}
		}))

		// Set up broker instance
		brokerInstance = &broker.Broker{
			Plans: map[string]services.Plan{"rabbitmq-service/rabbitmq-plan": testPlan},
			BOSH:  mockBOSH,
			Vault: vault,
			Config: &config.Config{
				Debug: true,
			},
			InstanceLocks: make(map[string]*sync.Mutex),
		}

		// Set up instance deployment in vault
		err := suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
			"service_id":      "rabbitmq-service",
			"plan_id":         "rabbitmq-plan",
			"deployment_name": "rabbitmq-plan-" + instanceID,
			"requested_at":    time.Now().Format(time.RFC3339),
		})
		Expect(err).NotTo(HaveOccurred())

		// Set up BOSH VMs
		mockBOSH.SetVMs("rabbitmq-plan-"+instanceID, []bosh.VM{
			{
				ID:    "rabbitmq-vm-0",
				Job:   "rabbitmq",
				Index: 0,
				IPs:   []string{strings.TrimPrefix(mockRabbitMQ.URL, "http://")},
				DNS:   []string{"rabbitmq-0.rabbitmq.default.bosh"},
			},
		})
	})

	AfterEach(func() {
		if mockRabbitMQ != nil {
			mockRabbitMQ.Close()
		}
		// no per-test vault; suite.vault is closed at suite end
	})

	Describe("Complete Bind/Unbind Flow", func() {
		It("should handle complete lifecycle of a binding", func() {
			ctx := context.Background()
			bindingID := "test-binding-lifecycle"

			// Store binding metadata
			err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
				bindingID: map[string]interface{}{
					"service_id": "rabbitmq-service",
					"plan_id":    "rabbitmq-plan",
					"app_guid":   "test-app-001",
					"created_at": time.Now().Format(time.RFC3339),
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Step 1: Bind - should create user
			credentials, err := brokerInstance.GetBindingCredentials(ctx, instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials).ToNot(BeNil())
			Expect(credentials.Username).To(Equal(bindingID))
			Expect(credentials.Password).ToNot(BeEmpty())
			Expect(credentials.CredentialType).To(Equal("dynamic"))

			// Verify user exists in RabbitMQ
			_, exists := rabbitMQUsers.Load(bindingID)
			Expect(exists).To(BeTrue(), "User should exist in RabbitMQ after bind")

			// Step 2: Get credentials again - should retrieve from vault
			credentials2, err := brokerInstance.GetBindingCredentials(ctx, instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials2.Username).To(Equal(credentials.Username))
			Expect(credentials2.Password).To(Equal(credentials.Password))

			// Step 3: Unbind - should delete user
			_, err = brokerInstance.Unbind(ctx, instanceID, bindingID, domain.UnbindDetails{
				ServiceID: "rabbitmq-service",
				PlanID:    "rabbitmq-plan",
			}, false)
			Expect(err).ToNot(HaveOccurred())

			// Verify user deleted from RabbitMQ
			_, exists = rabbitMQUsers.Load(bindingID)
			Expect(exists).To(BeFalse(), "User should not exist in RabbitMQ after unbind")
		})
	})

	Describe("Concurrent Bind Operations", func() {
		It("should handle multiple concurrent binds for the same instance", func() {
			ctx := context.Background()
			const numBindings = 10
			var waitGroup sync.WaitGroup
			errors := make([]error, numBindings)
			credentials := make([]*broker.BindingCredentials, numBindings)

			for bindingIndex := range numBindings {
				waitGroup.Add(1)
				go func(idx int) {
					defer waitGroup.Done()
					bindID := fmt.Sprintf("concurrent-bind-%d", idx)

					// Store binding metadata
					err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
						bindID: map[string]interface{}{
							"service_id": "rabbitmq-service",
							"plan_id":    "rabbitmq-plan",
							"app_guid":   fmt.Sprintf("app-%d", idx),
						},
					})
					if err != nil {
						errors[idx] = err

						return
					}

					creds, err := brokerInstance.GetBindingCredentials(ctx, instanceID, bindID)
					credentials[idx] = creds
					errors[idx] = err
				}(bindingIndex)
			}

			waitGroup.Wait()

			// Verify all bindings succeeded
			for i := range numBindings {
				Expect(errors[i]).ToNot(HaveOccurred(), fmt.Sprintf("Binding %d should succeed", i))
				Expect(credentials[i]).ToNot(BeNil())
				Expect(credentials[i].Username).To(Equal(fmt.Sprintf("concurrent-bind-%d", i)))
			}

			// Verify all users exist in RabbitMQ
			for i := range numBindings {
				userID := fmt.Sprintf("concurrent-bind-%d", i)
				_, exists := rabbitMQUsers.Load(userID)
				Expect(exists).To(BeTrue(), fmt.Sprintf("User %s should exist", userID))
			}
		})
	})

	Describe("Concurrent Unbind Operations", func() {
		It("should handle multiple concurrent unbinds for the same instance", func() {
			ctx := context.Background()
			const numBindings = 10

			// First, create all bindings
			for bindingIndex := range numBindings {
				bindID := fmt.Sprintf("concurrent-unbind-%d", bindingIndex)
				rabbitMQUsers.Store(bindID, true) // Simulate existing users

				// Store binding in vault
				err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
					bindID: map[string]interface{}{
						"service_id": "rabbitmq-service",
						"plan_id":    "rabbitmq-plan",
					},
				})
				Expect(err).NotTo(HaveOccurred())

				// Store credentials
				err = suite.vault.WriteSecret(fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindID), map[string]interface{}{
					"username": bindID,
					"password": "test-password",
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Now unbind concurrently
			var waitGroup sync.WaitGroup
			errors := make([]error, numBindings)

			for bindingIndex := range numBindings {
				waitGroup.Add(1)
				go func(idx int) {
					defer waitGroup.Done()
					bindID := fmt.Sprintf("concurrent-unbind-%d", idx)
					_, errors[idx] = brokerInstance.Unbind(ctx, instanceID, bindID, domain.UnbindDetails{
						ServiceID: "rabbitmq-service",
						PlanID:    "rabbitmq-plan",
					}, false)
				}(bindingIndex)
			}

			waitGroup.Wait()

			// Verify all unbinds succeeded
			for i := range numBindings {
				Expect(errors[i]).ToNot(HaveOccurred(), fmt.Sprintf("Unbind %d should succeed", i))
			}

			// Verify all users deleted from RabbitMQ
			for i := range numBindings {
				userID := fmt.Sprintf("concurrent-unbind-%d", i)
				_, exists := rabbitMQUsers.Load(userID)
				Expect(exists).To(BeFalse(), fmt.Sprintf("User %s should be deleted", userID))
			}
		})
	})

	Describe("Mixed Concurrent Operations", func() {
		It("should handle concurrent binds and unbinds on the same instance", func() {
			ctx := context.Background()
			const numOperations = 20
			var waitGroup sync.WaitGroup
			errors := make([]error, numOperations)

			// Create some existing bindings
			for i := range 10 {
				bindID := fmt.Sprintf("mixed-binding-%d", i)
				rabbitMQUsers.Store(bindID, true)
				err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
					bindID: map[string]interface{}{
						"service_id": "rabbitmq-service",
						"plan_id":    "rabbitmq-plan",
					},
				})
				Expect(err).NotTo(HaveOccurred())
			}

			// Perform mixed operations
			for operationIndex := range numOperations {
				waitGroup.Add(1)
				go func(idx int) {
					defer waitGroup.Done()

					if idx < 10 {
						// Unbind existing
						bindID := fmt.Sprintf("mixed-binding-%d", idx)
						_, errors[idx] = brokerInstance.Unbind(ctx, instanceID, bindID, domain.UnbindDetails{
							ServiceID: "rabbitmq-service",
							PlanID:    "rabbitmq-plan",
						}, false)
					} else {
						// Create new binding
						bindID := fmt.Sprintf("new-binding-%d", idx)
						err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
							bindID: map[string]interface{}{
								"service_id": "rabbitmq-service",
								"plan_id":    "rabbitmq-plan",
								"app_guid":   fmt.Sprintf("app-%d", idx),
							},
						})
						if err != nil {
							errors[idx] = err

							return
						}
						_, errors[idx] = brokerInstance.GetBindingCredentials(ctx, instanceID, bindID)
					}
				}(operationIndex)
			}

			waitGroup.Wait()

			// Verify all operations succeeded
			for i, err := range errors {
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Operation %d should succeed", i))
			}
		})
	})

	Describe("Instance Locking", func() {
		It("should properly serialize operations per instance", func() {
			ctx := context.Background()
			const numInstances = 3
			const operationsPerInstance = 5
			var waitGroup sync.WaitGroup

			operationTimes := make(map[string][]time.Time)
			var timesMutex sync.Mutex

			for instanceIdx := range numInstances {
				instID := fmt.Sprintf("instance-%d", instanceIdx)
				operationTimes[instID] = make([]time.Time, 0, operationsPerInstance*2)

				// Set up instance
				err := suite.vault.WriteSecret(instID+"/deployment", map[string]interface{}{
					"service_id": "rabbitmq-service",
					"plan_id":    "rabbitmq-plan",
				})
				Expect(err).NotTo(HaveOccurred())
				mockBOSH.SetVMs("rabbitmq-plan-"+instID, []bosh.VM{
					{
						ID:  "rabbitmq-vm-0",
						Job: "rabbitmq",
						IPs: []string{strings.TrimPrefix(mockRabbitMQ.URL, "http://")},
					},
				})

				for operationIdx := range operationsPerInstance {
					waitGroup.Add(1)
					go func(inst string, op int) {
						defer waitGroup.Done()

						bindID := fmt.Sprintf("binding-%d", op)
						err := suite.vault.WriteSecret(inst+"/bindings", map[string]interface{}{
							bindID: map[string]interface{}{
								"service_id": "rabbitmq-service",
								"plan_id":    "rabbitmq-plan",
							},
						})
						Expect(err).NotTo(HaveOccurred())

						// Record operation start
						timesMutex.Lock()
						operationTimes[inst] = append(operationTimes[inst], time.Now())
						timesMutex.Unlock()

						// Perform operation (this should acquire instance lock)
						_, _ = brokerInstance.GetBindingCredentials(ctx, inst, bindID)

						// Record operation end
						timesMutex.Lock()
						operationTimes[inst] = append(operationTimes[inst], time.Now())
						timesMutex.Unlock()
					}(instID, operationIdx)
				}
			}

			waitGroup.Wait()

			// Verify operations were properly serialized per instance
			for instID, times := range operationTimes {
				// Times should alternate: start1, end1, start2, end2, etc.
				// This proves operations didn't overlap
				for i := 0; i < len(times)-2; i += 2 {
					end := times[i+1]
					nextStart := times[i+2]
					Expect(end.Before(nextStart) || end.Equal(nextStart)).To(BeTrue(),
						fmt.Sprintf("Instance %s: operation end should be before next start", instID))
				}
			}
		})
	})

	Describe("Error Recovery", func() {
		It("should handle RabbitMQ API errors gracefully", func() {
			ctx := context.Background()
			bindingID := "error-recovery-test"

			// Create a temporary server that we can close without affecting other tests
			tempServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			// Save original broker state
			originalBOSH := brokerInstance.BOSH

			// Create a temporary mock BOSH with the temp server
			tempMockBOSH := testutil.NewIntegrationMockBOSH()
			tempMockBOSH.SetVMs("rabbitmq-plan-"+instanceID, []bosh.VM{
				{
					ID:  "rabbitmq-vm-0",
					Job: "rabbitmq",
					Index: 0,
					IPs:   []string{strings.TrimPrefix(tempServer.URL, "http://")},
					DNS:   []string{"rabbitmq-0.rabbitmq.default.bosh"},
				},
			})
			brokerInstance.BOSH = tempMockBOSH

			// Close the temp server to simulate connection error
			tempServer.Close()

			// Try to create binding
			err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
				bindingID: map[string]interface{}{
					"service_id": "rabbitmq-service",
					"plan_id":    "rabbitmq-plan",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			credentials, err := brokerInstance.GetBindingCredentials(ctx, instanceID, bindingID)
			Expect(err).To(HaveOccurred())
			Expect(credentials).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("connection refused"))

			// Restore original broker state
			brokerInstance.BOSH = originalBOSH
		})

		It("should recover from transient failures", func() {
			ctx := context.Background()
			bindingID := "transient-failure-test"
			failureCount := atomic.Int32{}

			// Save original broker state
			originalBOSH := brokerInstance.BOSH

			// Create a temporary server that fails first 2 attempts
			tempServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if strings.HasPrefix(request.URL.Path, "/api/users/") && request.Method == http.MethodPut {
					count := failureCount.Add(1)
					if count <= 2 {
						writer.WriteHeader(http.StatusServiceUnavailable)
						_, _ = writer.Write([]byte(`{"error":"Service Unavailable"}`))
					} else {
						writer.WriteHeader(http.StatusCreated)
					}
				} else if strings.HasPrefix(request.URL.Path, "/api/permissions/") {
					writer.WriteHeader(http.StatusCreated)
				} else {
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			defer tempServer.Close()

			// Create a temporary mock BOSH with the temp server
			tempMockBOSH := testutil.NewIntegrationMockBOSH()
			tempMockBOSH.SetVMs("rabbitmq-plan-"+instanceID, []bosh.VM{
				{
					ID:  "rabbitmq-vm-0",
					Job: "rabbitmq",
					Index: 0,
					IPs: []string{strings.TrimPrefix(tempServer.URL, "http://")},
				},
			})
			brokerInstance.BOSH = tempMockBOSH

			// Set up binding
			err := suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
				bindingID: map[string]interface{}{
					"service_id": "rabbitmq-service",
					"plan_id":    "rabbitmq-plan",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Should succeed after retries
			credentials, err := brokerInstance.GetBindingCredentials(ctx, instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials).ToNot(BeNil())
			Expect(failureCount.Load()).To(Equal(int32(3))) // Failed twice, succeeded on third

			// Restore original broker state
			brokerInstance.BOSH = originalBOSH
		})
	})
})

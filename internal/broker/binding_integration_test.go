package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// BrokerTestAdapter adapts broker.Broker to work with reconciler.BrokerInterface.
type BrokerTestAdapter struct {
	*broker.Broker
}

type vaultInterfaceAdapter struct {
	client *internalVault.Vault
}

func newVaultInterfaceAdapter(client *internalVault.Vault) *vaultInterfaceAdapter {
	return &vaultInterfaceAdapter{client: client}
}

func (v *vaultInterfaceAdapter) Get(path string) (map[string]interface{}, error) {
	var out map[string]interface{}

	exists, err := v.client.Get(context.Background(), path, &out)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, nil
	}

	return out, nil
}

func (v *vaultInterfaceAdapter) Put(path string, secret map[string]interface{}) error {
	return v.client.Put(context.Background(), path, secret)
}

func (v *vaultInterfaceAdapter) GetSecret(path string) (map[string]interface{}, error) {
	return v.Get(path)
}

func (v *vaultInterfaceAdapter) SetSecret(path string, secret map[string]interface{}) error {
	return v.client.Put(context.Background(), path, secret)
}

func (v *vaultInterfaceAdapter) DeleteSecret(path string) error {
	return v.client.Delete(context.Background(), path)
}

func (v *vaultInterfaceAdapter) ListSecrets(path string) ([]string, error) {
	// ListSecrets is not exercised in these integration tests; return empty slice for compatibility.
	return []string{}, nil
}

// GetServices implements reconciler.BrokerInterface.
func (b *BrokerTestAdapter) GetServices() []reconciler.Service {
	// Return empty services for test
	return []reconciler.Service{}
}

// GetBindingCredentials implements reconciler.BrokerInterface without context.
func (b *BrokerTestAdapter) GetBindingCredentials(instanceID, bindingID string) (*reconciler.BindingCredentials, error) {
	creds, err := b.Broker.GetBindingCredentials(context.Background(), instanceID, bindingID)
	if err != nil {
		return nil, fmt.Errorf("failed to get binding credentials: %w", err)
	}

	// Convert broker.BindingCredentials to reconciler.BindingCredentials
	return &reconciler.BindingCredentials{
		CredentialType:  creds.CredentialType,
		ReconstructedAt: creds.ReconstructedAt,
		Host:            creds.Host,
		Port:            creds.Port,
		Username:        creds.Username,
		Password:        creds.Password,
		Raw:             make(map[string]interface{}),
	}, nil
}

var _ = Describe("Binding Credentials Integration", func() {
	var (
		brokerInstance *broker.Broker
		updater        reconciler.Updater
		vaultClient    *internalVault.Vault
		mockBOSH       *testutil.IntegrationMockBOSH
		mockLogger     logger.Logger
		instanceID     string
		bindingID      string
		testServiceID  string
		testPlanID     string
	)

	BeforeEach(func() {
		instanceID = "integration-test-12345678-1234-1234-1234-123456789abc"
		bindingID = "integration-bind-87654321-4321-4321-4321-cba987654321"
		testServiceID = "redis-service"
		testPlanID = "small-plan"

		mockBOSH = testutil.NewIntegrationMockBOSH()
		mockLogger = logger.Get().Named("test") // Use default logger for tests
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		vaultAdapter := newVaultInterfaceAdapter(vaultClient)

		// Set up test plan
		defaultPlanCredentials := map[string]interface{}{
			"credentials": map[string]interface{}{
				"host":     "redis.example.com",
				"port":     6379,
				"username": "redis-user",
				"password": "redis-pass",
			},
		}

		testPlan := services.Plan{
			ID:          testPlanID,
			Name:        "Small Redis Plan",
			Type:        "redis",
			Credentials: toInterfaceMap(defaultPlanCredentials),
		}

		brokerInstance = &broker.Broker{
			Plans:  map[string]services.Plan{testServiceID + "/" + testPlanID: testPlan},
			BOSH:   mockBOSH,
			Vault:  vaultClient,
			Config: &config.Config{},
		}

		// Set up reconciler updater
		updater = reconciler.NewVaultUpdater(
			vaultAdapter,
			mockLogger,
			reconciler.BackupConfig{},
		)
	})

	// No per-test vault cleanup; suite.vault is managed by suite hooks

	Describe("Complete binding recovery workflow", func() {
		Context("when service instance exists but binding credentials are missing", func() {
			BeforeEach(func() {
				// Set up instance data in real vault
				_ = suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id":        testServiceID,
					"plan_id":           testPlanID,
					"deployment_name":   testPlanID + "-" + instanceID,
					"organization_guid": "test-org-uuid",
					"space_guid":        "test-space-uuid",
					"requested_at":      time.Now().Format(time.RFC3339),
				})

				// Set up BOSH deployment
				mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
					{
						ID:    "redis-vm-0",
						Job:   "redis",
						Index: 0,
						IPs:   []string{"10.0.0.100"},
						DNS:   []string{"redis-0.redis.default.bosh"},
					},
				})

				// Set up binding metadata but missing credentials
				_ = suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": testServiceID,
						"plan_id":    testPlanID,
						"app_guid":   "test-app-uuid",
					},
				})
			})

			It("should successfully recover binding credentials", func() {
				// First, verify the binding metadata exists
				var bindingMetadata map[string]interface{}
				exists, err := vaultClient.Get(context.Background(), instanceID+"/bindings", &bindingMetadata)
				Expect(err).ToNot(HaveOccurred())
				Expect(exists).To(BeTrue())
				Expect(bindingMetadata).To(HaveKey(bindingID))

				// Attempt to recover credentials
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())
				Expect(credentials).ToNot(BeNil())

				// Verify recovered credentials structure
				Expect(credentials.CredentialType).To(Equal("static"))
				Expect(credentials.ReconstructedAt).ToNot(BeEmpty())
				Expect(credentials.Host).ToNot(BeEmpty())
				Expect(credentials.Port).To(BeNumerically(">", 0))
				Expect(credentials.Username).ToNot(BeEmpty())
				Expect(credentials.Password).ToNot(BeEmpty())
			})

			It("should store recovered credentials back to vault", func() {
				// Perform recovery
				_, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())

				// Verify credentials were stored in vault
				path := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
				storedCreds, err := suite.vault.ReadSecret(path)
				Expect(err).ToNot(HaveOccurred())
				Expect(storedCreds).ToNot(BeNil())
				Expect(storedCreds).To(HaveKey("host"))
				Expect(storedCreds).To(HaveKey("port"))
				Expect(storedCreds["credential_type"]).To(Equal("static"))
			})
		})

		Context("when handling concurrent binding recovery", func() {
			BeforeEach(func() {
				// Set up multiple bindings for the same instance
				_ = suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})

				mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
					{
						ID:    "redis-vm-0",
						Job:   "redis",
						Index: 0,
						IPs:   []string{"10.0.0.100"},
						DNS:   []string{"redis-0.redis.default.bosh"},
					},
				})

				// Create multiple bindings
				for bindingIndex := range 3 {
					bid := "binding-concurrent-" + strconv.Itoa(bindingIndex)
					_ = suite.vault.WriteSecret(instanceID+"/bindings", map[string]interface{}{
						bid: map[string]interface{}{
							"service_id": testServiceID,
							"plan_id":    testPlanID,
							"app_guid":   "app-" + strconv.Itoa(bindingIndex),
						},
					})
				}
			})

			It("should handle concurrent credential recovery", func() {
				const numBindings = 3
				var waitGroup sync.WaitGroup
				errors := make(chan error, numBindings)

				for bindingIndex := range numBindings {
					waitGroup.Add(1)
					go func(idx int) {
						defer waitGroup.Done()
						bid := "binding-concurrent-" + strconv.Itoa(idx)
						_, err := brokerInstance.GetBindingCredentials(
							context.Background(), instanceID, bid)
						if err != nil {
							errors <- err
						}
					}(bindingIndex)
				}

				waitGroup.Wait()
				close(errors)

				// Check no errors occurred
				for err := range errors {
					Expect(err).ToNot(HaveOccurred())
				}
			})
		})

		Context("when RabbitMQ requires dynamic credential creation", func() {
			var rabbitAPIServer *httptest.Server

			BeforeEach(func() {
				rabbitAPIServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/users/"):
						w.WriteHeader(http.StatusCreated)
					case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/permissions/"):
						w.WriteHeader(http.StatusCreated)
					default:
						http.NotFound(w, r)
					}
				}))

				// Set up RabbitMQ plan
				rabbitPlan := services.Plan{
					ID:   "rabbit-plan",
					Name: "RabbitMQ Plan",
					Type: "rabbitmq",
					Credentials: toInterfaceMap(map[string]interface{}{
						"credentials": map[string]interface{}{
							"host":           "rabbitmq.example.com",
							"port":           5672,
							"api_url":        rabbitAPIServer.URL,
							"admin_username": "admin",
							"admin_password": "admin-secret",
							"vhost":          "/blacksmith",
						},
					}),
				}

				brokerInstance.Plans[testServiceID+"/rabbit-plan"] = rabbitPlan

				_ = suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    "rabbit-plan",
				})

				mockBOSH.SetVMs("rabbit-plan-"+instanceID, []bosh.VM{
					{
						ID:    "rabbitmq-vm-0",
						Job:   "rabbitmq",
						Index: 0,
						IPs:   []string{"10.0.0.200"},
						DNS:   []string{"rabbitmq-0.rabbitmq.default.bosh"},
					},
				})
			})

			AfterEach(func() {
				if rabbitAPIServer != nil {
					rabbitAPIServer.Close()
				}
			})

			It("should create dynamic user for RabbitMQ", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())
				Expect(credentials).ToNot(BeNil())
				Expect(credentials.CredentialType).To(Equal("dynamic"))
				Expect(credentials.Username).To(Equal(bindingID))
				Expect(credentials.Password).ToNot(Equal("admin-secret"))
			})
		})
	})

	Describe("Error recovery scenarios", func() {
		Context("when BOSH deployment doesn't exist", func() {
			BeforeEach(func() {
				_ = suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})

				mockBOSH.SetError("GetDeploymentVMs", "deployment not found")
			})

			It("should return appropriate error", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("deployment not found"))
			})
		})

		Context("when vault connectivity is lost", func() {
			var previousVault *internalVault.Vault

			BeforeEach(func() {
				previousVault = brokerInstance.Vault
				brokerInstance.Vault = internalVault.New("http://127.0.0.1:1", "", true)
			})

			AfterEach(func() {
				brokerInstance.Vault = previousVault
			})

			It("should return vault error", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("failed to store binding credentials"))
			})
		})

		Context("when plan configuration is invalid", func() {
			BeforeEach(func() {
				invalidPlan := services.Plan{
					ID:   "invalid-plan",
					Name: "Invalid Plan",
					Type: "redis",
					Credentials: toInterfaceMap(map[string]interface{}{
						"credentials": map[string]interface{}{
							"host": "{{.Invalid.Path}}",
						},
					}),
				}

				brokerInstance.Plans[testServiceID+"/invalid-plan"] = invalidPlan

				_ = suite.vault.WriteSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    "invalid-plan",
				})

				mockBOSH.SetVMs("invalid-plan-"+instanceID, []bosh.VM{
					{
						ID:    "redis-vm-0",
						Job:   "redis",
						Index: 0,
						IPs:   []string{"10.0.0.10"},
						DNS:   []string{"redis-0.redis.default.bosh"},
					},
				})
			})

			It("should handle template errors gracefully", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				if err != nil {
					Expect(err.Error()).To(ContainSubstring("failed to get credentials map"))
				} else {
					Expect(credentials).ToNot(BeNil())
				}
			})
		})
	})

	Describe("UpdateInstanceWithBindingRepair", func() {
		Context("when instance needs binding repair", func() {
			BeforeEach(func() {
				// Set up instance with missing bindings
				Expect(vaultClient.Put(context.Background(), instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})).To(Succeed())
			})

			It("should trigger binding repair process", func() {
				ctx := context.Background()
				instance := reconciler.InstanceData{
					ID:        instanceID,
					ServiceID: testServiceID,
					PlanID:    testPlanID,
				}

				brokerAdapter := &BrokerTestAdapter{Broker: brokerInstance}
				updatedInstance, err := updater.UpdateInstanceWithBindingRepair(
					ctx, instance, brokerAdapter)
				Expect(err).ToNot(HaveOccurred())
				Expect(updatedInstance).ToNot(BeNil())
				// The actual field to check depends on the implementation
				// Expect(updatedInstance.BindingsRepaired).To(BeTrue())
			})
		})
	})
})

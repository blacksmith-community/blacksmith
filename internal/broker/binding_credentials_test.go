package broker_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// Static errors for test err113 compliance.
var (
	ErrBOSHConnectionFailed  = errors.New("BOSH connection failed")
	ErrVaultConnectionFailed = errors.New("vault connection failed")
)

var _ = Describe("GetBindingCredentials", func() {
	var (
		brokerInstance *broker.Broker
		mockBOSH       *testutil.MockBOSHDirector
		mockConfig     *config.Config
		vaultClient    *internalVault.Vault
		instanceID     string
		bindingID      string
		testServiceID  string
		testPlanID     string
		cleanupPaths   map[string]struct{}
		writeSecret    func(path string, data map[string]interface{})
		recordCleanup  func(path string)
	)

	BeforeEach(func() {
		instanceID = "test-instance-12345678-1234-1234-1234-123456789abc"
		bindingID = "test-binding-87654321-4321-4321-4321-cba987654321"
		testServiceID = "redis-service"
		testPlanID = "small-plan"

		cleanupPaths = map[string]struct{}{}
		recordCleanup = func(path string) {
			cleanupPaths[path] = struct{}{}
		}
		writeSecret = func(path string, data map[string]interface{}) {
			Expect(suite.vault.WriteSecret(path, data)).To(Succeed())
			recordCleanup(path)
		}

		mockBOSH = testutil.NewMockBOSHDirector()
		mockConfig = &config.Config{}
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

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
			Config: mockConfig,
		}

		recordCleanup(instanceID + "/bindings/" + bindingID + "/credentials")
	})

	AfterEach(func() {
		for path := range cleanupPaths {
			_ = suite.vault.DeleteSecret(path)
		}
		brokerInstance.Vault = vaultClient
	})

	Describe("Basic credential reconstruction", func() {
		Context("when instance and plan data exists", func() {
			BeforeEach(func() {
				writeSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})

				mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
					{
						ID:    "redis-vm-0",
						Job:   "redis",
						Index: 0,
						IPs:   []string{"10.0.0.10"},
						DNS:   []string{"redis-0.redis.default.bosh"},
					},
				})
			})

			It("should successfully reconstruct static credentials", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())
				Expect(credentials).ToNot(BeNil())
				Expect(credentials.CredentialType).To(Equal("static"))
				Expect(credentials.ReconstructedAt).ToNot(BeEmpty())
			})

			It("should include reconstructed timestamp", func() {
				before := time.Now()
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				after := time.Now()

				Expect(err).ToNot(HaveOccurred())
				reconstructedTime, err := time.Parse(time.RFC3339, credentials.ReconstructedAt)
				Expect(err).ToNot(HaveOccurred())
				Expect(reconstructedTime).To(BeTemporally(">=", before.Add(-time.Second)))
				Expect(reconstructedTime).To(BeTemporally("<=", after.Add(time.Second)))
			})
		})

		Context("when instance data is missing", func() {
			It("should return an error", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), "nonexistent-instance", bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("failed to get service/plan info"))
			})
		})

		Context("when plan is not found", func() {
			BeforeEach(func() {
				writeSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": "unknown-service",
					"plan_id":    "unknown-plan",
				})
			})

			It("should return an error", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("failed to find plan"))
			})
		})
	})

	Describe("RabbitMQ dynamic credentials", func() {
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

			writeSecret(instanceID+"/deployment", map[string]interface{}{
				"service_id": testServiceID,
				"plan_id":    testPlanID,
			})

			testPlanKey := testServiceID + "/" + testPlanID
			testPlan := brokerInstance.Plans[testPlanKey]
			testPlan.Credentials = toInterfaceMap(map[string]interface{}{
				"credentials": map[string]interface{}{
					"host":           "rabbitmq.example.com",
					"port":           5672,
					"username":       "static-user",
					"password":       "static-pass",
					"api_url":        rabbitAPIServer.URL,
					"admin_username": "admin",
					"admin_password": "admin-pass",
					"vhost":          "/test-vhost",
				},
			})
			brokerInstance.Plans[testPlanKey] = testPlan

			mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
				{
					ID:    "rabbitmq-vm-0",
					Job:   "rabbitmq",
					Index: 0,
					IPs:   []string{"10.0.0.20"},
					DNS:   []string{"rabbitmq-0.rabbitmq.default.bosh"},
				},
			})
		})

		AfterEach(func() {
			if rabbitAPIServer != nil {
				rabbitAPIServer.Close()
			}
		})

		It("should create dynamic RabbitMQ credentials", func() {
			credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials).ToNot(BeNil())
			Expect(credentials.CredentialType).To(Equal("dynamic"))
			Expect(credentials.Username).To(Equal(bindingID))
			Expect(credentials.Password).ToNot(Equal("static-pass"))
			Expect(credentials.APIURL).To(Equal(rabbitAPIServer.URL))
			Expect(credentials.Vhost).To(Equal("/test-vhost"))
		})

		It("should preserve host and port information", func() {
			credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials.Host).To(Equal("rabbitmq.example.com"))
			Expect(credentials.Port).To(Equal(5672))
		})

		It("should not include admin credentials in response", func() {
			credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials.Raw).ToNot(HaveKey("admin_username"))
			Expect(credentials.Raw).ToNot(HaveKey("admin_password"))
		})
	})

	Describe("Credential structure population", func() {
		BeforeEach(func() {
			testCredentials := map[string]interface{}{
				"host":     "db.example.com",
				"port":     float64(5432), // JSON unmarshals numbers as float64
				"username": "dbuser",
				"password": "dbpass",
				"database": "testdb",
				"uri":      "postgres://dbuser:dbpass@db.example.com:5432/testdb",
				"scheme":   "postgres",
			}

			writeSecret(instanceID+"/deployment", map[string]interface{}{
				"service_id": testServiceID,
				"plan_id":    testPlanID,
			})

			testPlanKey := testServiceID + "/" + testPlanID
			plan := brokerInstance.Plans[testPlanKey]
			plan.Credentials = toInterfaceMap(map[string]interface{}{
				"credentials": testCredentials,
			})
			brokerInstance.Plans[testPlanKey] = plan

			mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
				{
					ID:    "postgres-vm-0",
					Job:   "postgres",
					Index: 0,
					IPs:   []string{"10.0.0.30"},
					DNS:   []string{"postgres-0.postgres.default.bosh"},
				},
			})
		})

		It("should populate all structured fields correctly", func() {
			credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials.Host).To(Equal("db.example.com"))
			Expect(credentials.Port).To(Equal(5432))
			Expect(credentials.Username).To(Equal("dbuser"))
			Expect(credentials.Password).To(Equal("dbpass"))
			Expect(credentials.Database).To(Equal("testdb"))
			Expect(credentials.URI).To(Equal("postgres://dbuser:dbpass@db.example.com:5432/testdb"))
			Expect(credentials.Scheme).To(Equal("postgres"))
		})

		It("should preserve raw credentials", func() {
			credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
			Expect(err).ToNot(HaveOccurred())
			Expect(credentials.Raw).To(HaveKey("host"))
			Expect(credentials.Raw).To(HaveKey("port"))
			Expect(credentials.Raw).To(HaveKey("database"))
			Expect(credentials.Raw).To(HaveKey("uri"))
		})
	})

	Describe("Error handling", func() {
		Context("when BOSH GetCreds fails", func() {
			BeforeEach(func() {
				writeSecret(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})

				mockBOSH.SetError(ErrBOSHConnectionFailed)
			})

			It("should return an error", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("failed to get deployment VMs"))
			})
		})

		Context("when vault operations fail", func() {
			var previousVault *internalVault.Vault

			BeforeEach(func() {
				previousVault = brokerInstance.Vault
				brokerInstance.Vault = internalVault.New("http://127.0.0.1:1", "", true)
			})

			AfterEach(func() {
				brokerInstance.Vault = previousVault
			})

			It("should return a connection error", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("failed to get service/plan info"))
			})
		})
	})

	Describe("Deployment VMs mapping", func() {
		BeforeEach(func() {
			writeSecret(instanceID+"/deployment", map[string]interface{}{
				"service_id": testServiceID,
				"plan_id":    testPlanID,
			})
		})

		Context("with multiple VMs", func() {
			BeforeEach(func() {
				mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
					{
						ID:    "db-vm-0",
						Job:   "postgres",
						Index: 0,
						IPs:   []string{"10.0.0.10", "192.168.1.10"},
						DNS:   []string{"db-0.postgres.default.bosh"},
					},
					{
						ID:    "db-vm-1",
						Job:   "postgres",
						Index: 1,
						IPs:   []string{"10.0.0.11", "192.168.1.11"},
						DNS:   []string{"db-1.postgres.default.bosh"},
					},
				})
			})

			It("should handle multiple VMs correctly", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())
				Expect(credentials).ToNot(BeNil())
			})
		})

		Context("with no VMs", func() {
			BeforeEach(func() {
				mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{})
			})

			It("should handle empty VM list", func() {
				credentials, err := brokerInstance.GetBindingCredentials(context.Background(), instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())
				Expect(credentials).ToNot(BeNil())
			})
		})
	})
})

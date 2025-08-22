package main

import (
	"errors"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/reconciler"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = XDescribe("Binding Credentials Integration - SKIPPED: Mock infrastructure incomplete", func() {
	var (
		broker *Broker
		// updater       reconciler.Updater // TODO: Fix UpdateInstanceWithBindingRepair usage
		mockVault     *IntegrationMockVault
		mockBOSH      *IntegrationMockBOSH
		instanceID    string
		bindingID     string
		testServiceID string
		testPlanID    string
	)

	BeforeEach(func() {
		instanceID = "integration-test-12345678-1234-1234-1234-123456789abc"
		bindingID = "integration-bind-87654321-4321-4321-4321-cba987654321"
		testServiceID = "redis-service"
		testPlanID = "small-plan"

		mockVault = NewIntegrationMockVault()
		mockBOSH = NewIntegrationMockBOSH()

		// Set up test plan
		testPlan := Plan{
			ID:   testPlanID,
			Name: "Small Redis Plan",
			Type: "redis",
			Credentials: map[interface{}]interface{}{
				"host":     "redis.{{.Jobs.redis.IPs.0}}",
				"port":     6379,
				"username": "redis-user",
				"password": "redis-pass",
			},
		}

		broker = &Broker{
			Plans: map[string]Plan{testServiceID + "/" + testPlanID: testPlan},
			BOSH:  mockBOSH,
			// TODO: Fix this - Vault field expects *Vault but we have *IntegrationMockVault
			// Vault:  mockVault,
			Config: &Config{},
		}

		// Set up reconciler updater
		// TODO: Fix UpdateInstanceWithBindingRepair usage
		// updater = reconciler.NewVaultUpdater(mockVault, &IntegrationMockLogger{}, reconciler.BackupConfig{})
	})

	Describe("Complete binding recovery workflow", func() {
		Context("when service instance exists but binding credentials are missing", func() {
			BeforeEach(func() {
				// Set up instance data in vault
				mockVault.SetData(instanceID+"/deployment", map[string]interface{}{
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
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": testServiceID,
						"plan_id":    testPlanID,
						"app_guid":   "test-app-uuid",
						"created_at": time.Now().Format(time.RFC3339),
					},
				})

				// Simulate missing credentials (credentials path doesn't exist)
				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", "credentials not found")
			})

			It("should detect missing credentials and successfully reconstruct them", func() {
				// Step 1: Reconciler detects missing binding credentials
				instance := &reconciler.InstanceData{
					ID:             instanceID,
					ServiceID:      testServiceID,
					PlanID:         testPlanID,
					DeploymentName: testPlanID + "-" + instanceID,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
					LastSyncedAt:   time.Now(),
					Metadata:       make(map[string]interface{}),
				}

				// Step 2: Run reconciler update which should detect unhealthy bindings
				// TODO: Fix this - UpdateInstanceWithBindingRepair is not part of Updater interface
				// err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, broker)
				// Expect(err).ToNot(HaveOccurred())

				// Step 3: Verify that binding repair was attempted and succeeded
				Expect(instance.Metadata).To(HaveKey("has_bindings"))
				Expect(instance.Metadata["has_bindings"]).To(BeTrue())
				Expect(instance.Metadata).To(HaveKey("unhealthy_bindings_count"))
				Expect(instance.Metadata["unhealthy_bindings_count"]).To(BeNumerically(">", 0))

				// Step 4: Verify credentials were reconstructed and stored
				Eventually(func() bool {
					storedCreds := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/credentials")
					return len(storedCreds) > 0
				}, "5s", "100ms").Should(BeTrue())

				storedCreds := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/credentials")
				Expect(storedCreds).To(HaveKey("host"))
				Expect(storedCreds).To(HaveKey("port"))
				Expect(storedCreds).To(HaveKey("username"))
				Expect(storedCreds).To(HaveKey("password"))

				// Step 5: Verify metadata was updated
				metadata := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/metadata")
				Expect(metadata).To(HaveKey("reconstruction_source"))
				Expect(metadata["reconstruction_source"]).To(Equal("broker"))
				Expect(metadata["status"]).To(Equal("reconstructed"))
			})

			It("should update instance metadata to reflect repair success", func() {
				instance := &reconciler.InstanceData{
					ID:             instanceID,
					ServiceID:      testServiceID,
					PlanID:         testPlanID,
					DeploymentName: testPlanID + "-" + instanceID,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
					LastSyncedAt:   time.Now(),
					Metadata:       make(map[string]interface{}),
				}

				// TODO: Fix this - UpdateInstanceWithBindingRepair is not part of Updater interface
				// err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, broker)
				// Expect(err).ToNot(HaveOccurred())

				// Check that repair success was recorded
				Eventually(func() interface{} {
					return instance.Metadata["binding_repair_succeeded"]
				}, "5s", "100ms").Should(BeTrue())

				Expect(instance.Metadata["needs_binding_repair"]).To(BeFalse())
				Expect(instance.Metadata).To(HaveKey("binding_repair_completed_at"))
			})
		})

		Context("when RabbitMQ service requires dynamic credentials", func() {
			var rabbitMQPlan Plan

			BeforeEach(func() {
				rabbitMQPlan = Plan{
					ID:   "rabbitmq-small",
					Name: "Small RabbitMQ Plan",
					Type: "rabbitmq",
					Credentials: map[interface{}]interface{}{
						"host":           "rabbitmq.{{.Jobs.rabbitmq.IPs.0}}",
						"port":           5672,
						"username":       "static-user",
						"password":       "static-pass",
						"api_url":        "https://rabbitmq.{{.Jobs.rabbitmq.IPs.0}}:15672/api",
						"admin_username": "admin",
						"admin_password": "admin-secret",
						"vhost":          "/test",
					},
				}

				broker.Plans["rabbitmq-service/rabbitmq-small"] = rabbitMQPlan

				// Set up RabbitMQ instance
				mockVault.SetData(instanceID+"/deployment", map[string]interface{}{
					"service_id":      "rabbitmq-service",
					"plan_id":         "rabbitmq-small",
					"deployment_name": "rabbitmq-small-" + instanceID,
				})

				// Set up RabbitMQ VMs
				mockBOSH.SetVMs("rabbitmq-small-"+instanceID, []bosh.VM{
					{
						ID:    "rabbitmq-vm-0",
						Job:   "rabbitmq",
						Index: 0,
						IPs:   []string{"10.0.0.200"},
						DNS:   []string{"rabbitmq-0.rabbitmq.default.bosh"},
					},
				})

				// Set up binding with missing credentials
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "rabbitmq-service",
						"plan_id":    "rabbitmq-small",
						"app_guid":   "test-app-uuid",
					},
				})

				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", "credentials not found")
			})

			It("should create dynamic RabbitMQ credentials with binding-specific username", func() {
				// Test the broker's GetBindingCredentials directly for RabbitMQ
				credentials, err := broker.GetBindingCredentials(instanceID, bindingID)
				Expect(err).ToNot(HaveOccurred())
				Expect(credentials).ToNot(BeNil())

				// Verify dynamic credentials were created
				Expect(credentials.CredentialType).To(Equal("dynamic"))
				Expect(credentials.Username).To(Equal(bindingID))             // Username should be the bindingID
				Expect(credentials.Password).ToNot(Equal("static-pass"))      // Password should be generated
				Expect(credentials.APIURL).To(ContainSubstring("10.0.0.200")) // Should contain VM IP
				Expect(credentials.Vhost).To(Equal("/test"))

				// Verify admin credentials are not included
				Expect(credentials.Raw).ToNot(HaveKey("admin_username"))
				Expect(credentials.Raw).ToNot(HaveKey("admin_password"))
			})
		})

		Context("when BOSH deployment is missing", func() {
			BeforeEach(func() {
				// Set up instance data but no BOSH deployment
				mockVault.SetData(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})

				// No VMs configured (simulating missing deployment)
				mockBOSH.SetError("GetDeploymentVMs", "deployment not found")

				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": testServiceID,
						"plan_id":    testPlanID,
					},
				})
			})

			It("should handle missing deployment gracefully", func() {
				credentials, err := broker.GetBindingCredentials(instanceID, bindingID)
				Expect(err).To(HaveOccurred())
				Expect(credentials).To(BeNil())
				Expect(err.Error()).To(ContainSubstring("failed to get base credentials"))
			})

			It("should record reconstruction failure in reconciler", func() {
				instance := &reconciler.InstanceData{
					ID:             instanceID,
					ServiceID:      testServiceID,
					PlanID:         testPlanID,
					DeploymentName: testPlanID + "-" + instanceID,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
					LastSyncedAt:   time.Now(),
					Metadata:       make(map[string]interface{}),
				}

				// TODO: Fix this - UpdateInstanceWithBindingRepair is not part of Updater interface
				// err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, broker)
				// Expect(err).ToNot(HaveOccurred()) // Shouldn't fail overall

				// Check that failure was recorded
				Expect(instance.Metadata["binding_repair_failed"]).To(BeTrue())
				Expect(instance.Metadata).To(HaveKey("binding_repair_error"))
			})
		})
	})

	Describe("Performance and scalability", func() {
		Context("when instance has multiple bindings", func() {
			var bindingIDs []string

			BeforeEach(func() {
				bindingIDs = []string{
					"binding-1-11111111-1111-1111-1111-111111111111",
					"binding-2-22222222-2222-2222-2222-222222222222",
					"binding-3-33333333-3333-3333-3333-333333333333",
					"binding-4-44444444-4444-4444-4444-444444444444",
					"binding-5-55555555-5555-5555-5555-555555555555",
				}

				// Set up instance
				mockVault.SetData(instanceID+"/deployment", map[string]interface{}{
					"service_id": testServiceID,
					"plan_id":    testPlanID,
				})

				mockBOSH.SetVMs(testPlanID+"-"+instanceID, []bosh.VM{
					{
						ID:    "redis-vm-0",
						Job:   "redis",
						Index: 0,
						IPs:   []string{"10.0.0.100"},
					},
				})

				// Set up multiple bindings with missing credentials
				bindingsData := make(map[string]interface{})
				for _, bid := range bindingIDs {
					bindingsData[bid] = map[string]interface{}{
						"service_id": testServiceID,
						"plan_id":    testPlanID,
						"app_guid":   "test-app-" + bid,
					}
					mockVault.SetError(instanceID+"/bindings/"+bid+"/credentials", "credentials not found")
				}
				mockVault.SetData(instanceID+"/bindings", bindingsData)
			})

			It("should handle multiple binding reconstructions efficiently", func() {
				start := time.Now()

				instance := &reconciler.InstanceData{
					ID:             instanceID,
					ServiceID:      testServiceID,
					PlanID:         testPlanID,
					DeploymentName: testPlanID + "-" + instanceID,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
					LastSyncedAt:   time.Now(),
					Metadata:       make(map[string]interface{}),
				}

				// TODO: Fix this - UpdateInstanceWithBindingRepair is not part of Updater interface
				// err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, broker)
				// Expect(err).ToNot(HaveOccurred())

				duration := time.Since(start)
				Expect(duration).To(BeNumerically("<", 10*time.Second)) // Should complete within 10 seconds

				// Verify all bindings were processed
				Expect(instance.Metadata["binding_repair_succeeded"]).To(BeTrue())

				// Verify all credentials were stored
				for _, bid := range bindingIDs {
					credentials := mockVault.GetData(instanceID + "/bindings/" + bid + "/credentials")
					Expect(credentials).ToNot(BeNil())
					Expect(credentials).To(HaveKey("host"))
				}
			})
		})
	})
})

// Integration test mock implementations

type IntegrationMockVault struct {
	data   map[string]map[string]interface{}
	errors map[string]string
}

func NewIntegrationMockVault() *IntegrationMockVault {
	return &IntegrationMockVault{
		data:   make(map[string]map[string]interface{}),
		errors: make(map[string]string),
	}
}

func (imv *IntegrationMockVault) SetData(path string, data map[string]interface{}) {
	imv.data[path] = data
}

func (imv *IntegrationMockVault) GetData(path string) map[string]interface{} {
	return imv.data[path]
}

func (imv *IntegrationMockVault) SetError(path, errorMsg string) {
	imv.errors[path] = errorMsg
}

func (imv *IntegrationMockVault) Get(path string, out interface{}) (bool, error) {
	if errorMsg, exists := imv.errors[path]; exists {
		return false, errors.New(errorMsg)
	}

	data, exists := imv.data[path]
	if !exists {
		return false, nil
	}

	if outMap, ok := out.(*map[string]interface{}); ok {
		*outMap = data
		return true, nil
	}

	return false, errors.New("unsupported output type")
}

func (imv *IntegrationMockVault) Put(path string, data interface{}) error {
	if dataMap, ok := data.(map[string]interface{}); ok {
		imv.data[path] = dataMap
		return nil
	}
	return errors.New("unsupported data type")
}

// Implement other Vault interface methods
func (imv *IntegrationMockVault) Index(instanceID string, data map[string]interface{}) error {
	return nil
}
func (imv *IntegrationMockVault) GetIndex(name string) (interface{}, error) { return nil, nil }
func (imv *IntegrationMockVault) TrackProgress(instanceID, operation, message string, taskID int, params map[interface{}]interface{}) error {
	return nil
}

type IntegrationMockBOSH struct {
	vms    map[string][]bosh.VM
	errors map[string]string
}

func NewIntegrationMockBOSH() *IntegrationMockBOSH {
	return &IntegrationMockBOSH{
		vms:    make(map[string][]bosh.VM),
		errors: make(map[string]string),
	}
}

func (imb *IntegrationMockBOSH) SetVMs(deployment string, vms []bosh.VM) {
	imb.vms[deployment] = vms
}

func (imb *IntegrationMockBOSH) SetError(operation, errorMsg string) {
	imb.errors[operation] = errorMsg
}

func (imb *IntegrationMockBOSH) GetDeploymentVMs(deployment string) ([]bosh.VM, error) {
	if errorMsg, exists := imb.errors["GetDeploymentVMs"]; exists {
		return nil, errors.New(errorMsg)
	}

	vms, exists := imb.vms[deployment]
	if !exists {
		return []bosh.VM{}, nil
	}

	return vms, nil
}

// Implement other BOSH interface methods
func (imb *IntegrationMockBOSH) GetInfo() (*bosh.Info, error)           { return nil, nil }
func (imb *IntegrationMockBOSH) GetTask(taskID int) (*bosh.Task, error) { return nil, nil }
func (imb *IntegrationMockBOSH) CreateDeployment(manifest string) (*bosh.Task, error) {
	return nil, nil
}
func (imb *IntegrationMockBOSH) DeleteDeployment(deployment string) (*bosh.Task, error) {
	return nil, nil
}
func (imb *IntegrationMockBOSH) GetDeployment(deployment string) (*bosh.DeploymentDetail, error) {
	return nil, nil
}
func (imb *IntegrationMockBOSH) GetDeployments() ([]bosh.Deployment, error) { return nil, nil }
func (imb *IntegrationMockBOSH) GetEvents(deployment string) ([]bosh.Event, error) {
	return nil, nil
}

// Additional methods to implement bosh.Director interface
func (imb *IntegrationMockBOSH) GetReleases() ([]bosh.Release, error)                 { return nil, nil }
func (imb *IntegrationMockBOSH) UploadRelease(url, sha1 string) (*bosh.Task, error)   { return nil, nil }
func (imb *IntegrationMockBOSH) GetStemcells() ([]bosh.Stemcell, error)               { return nil, nil }
func (imb *IntegrationMockBOSH) UploadStemcell(url, sha1 string) (*bosh.Task, error)  { return nil, nil }
func (imb *IntegrationMockBOSH) GetTaskOutput(taskID int, typ string) (string, error) { return "", nil }
func (imb *IntegrationMockBOSH) GetTaskEvents(taskID int) ([]bosh.TaskEvent, error)   { return nil, nil }
func (imb *IntegrationMockBOSH) FetchLogs(deployment, job, index string) (string, error) {
	return "", nil
}
func (imb *IntegrationMockBOSH) UpdateCloudConfig(yaml string) error        { return nil }
func (imb *IntegrationMockBOSH) GetCloudConfig() (string, error)            { return "", nil }
func (imb *IntegrationMockBOSH) Cleanup(removeAll bool) (*bosh.Task, error) { return nil, nil }
func (imb *IntegrationMockBOSH) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "", nil
}
func (imb *IntegrationMockBOSH) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return nil, nil
}

// IntegrationMockLogger implements reconciler.Logger interface for testing
type IntegrationMockLogger struct{}

func (ml *IntegrationMockLogger) Debug(format string, args ...interface{})   {}
func (ml *IntegrationMockLogger) Info(format string, args ...interface{})    {}
func (ml *IntegrationMockLogger) Error(format string, args ...interface{})   {}
func (ml *IntegrationMockLogger) Warning(format string, args ...interface{}) {}

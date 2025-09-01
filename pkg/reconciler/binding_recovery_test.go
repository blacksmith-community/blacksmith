package reconciler

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Binding Recovery", func() {
	var (
		updater    *vaultUpdater
		mockVault  *MockVaultUpdater
		mockBroker *MockBroker
		instanceID string
		bindingID  string
	)

	BeforeEach(func() {
		instanceID = "test-instance-12345678-1234-1234-1234-123456789abc"
		bindingID = "test-binding-87654321-4321-4321-4321-cba987654321"

		mockVault = NewMockVaultUpdater()
		mockBroker = NewMockBroker()

		updater = &vaultUpdater{
			vault:        mockVault,
			logger:       NewMockLogger(),
			backupConfig: BackupConfig{Enabled: false},
		}
	})

	Describe("checkBindingHealth", func() {
		Context("when instance has no bindings", func() {
			BeforeEach(func() {
				mockVault.SetError(instanceID+"/bindings", errors.New("not found"))
			})

			It("should return empty results without error", func() {
				healthy, unhealthy, err := updater.checkBindingHealth(instanceID)
				Expect(err).ToNot(HaveOccurred())
				Expect(healthy).To(BeEmpty())
				Expect(unhealthy).To(BeEmpty())
			})
		})

		Context("when instance has healthy bindings", func() {
			BeforeEach(func() {
				// Set up bindings index
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})

				// Set up healthy credentials
				mockVault.SetData(instanceID+"/bindings/"+bindingID+"/credentials", map[string]interface{}{
					"host":     "redis.example.com",
					"port":     6379,
					"username": "redis-user",
					"password": "redis-pass",
				})
			})

			It("should identify healthy bindings", func() {
				healthy, unhealthy, err := updater.checkBindingHealth(instanceID)
				Expect(err).ToNot(HaveOccurred())
				Expect(healthy).To(HaveLen(1))
				Expect(unhealthy).To(BeEmpty())
				Expect(healthy[0].ID).To(Equal(bindingID))
				Expect(healthy[0].Status).To(Equal("healthy"))
			})
		})

		Context("when instance has unhealthy bindings", func() {
			BeforeEach(func() {
				// Set up bindings index
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})

				// No credentials data (simulating missing credentials)
				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", errors.New("not found"))
			})

			It("should identify unhealthy bindings", func() {
				healthy, unhealthy, err := updater.checkBindingHealth(instanceID)
				Expect(err).ToNot(HaveOccurred())
				Expect(healthy).To(BeEmpty())
				Expect(unhealthy).To(HaveLen(1))
				Expect(unhealthy[0]).To(Equal(bindingID))
			})
		})

		Context("when instance has mixed binding health", func() {
			var healthyBindingID string

			BeforeEach(func() {
				healthyBindingID = "healthy-binding-11111111-1111-1111-1111-111111111111"

				// Set up bindings index with two bindings
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
					healthyBindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})

				// Healthy binding has credentials
				mockVault.SetData(instanceID+"/bindings/"+healthyBindingID+"/credentials", map[string]interface{}{
					"host":     "redis.example.com",
					"port":     6379,
					"username": "redis-user",
					"password": "redis-pass",
				})

				// Unhealthy binding missing credentials
				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", errors.New("not found"))
			})

			It("should correctly categorize bindings", func() {
				healthy, unhealthy, err := updater.checkBindingHealth(instanceID)
				Expect(err).ToNot(HaveOccurred())
				Expect(healthy).To(HaveLen(1))
				Expect(unhealthy).To(HaveLen(1))
				Expect(healthy[0].ID).To(Equal(healthyBindingID))
				Expect(unhealthy[0]).To(Equal(bindingID))
			})
		})
	})

	Describe("ReconstructBindingWithBroker", func() {
		var mockCredentials *BindingCredentials

		BeforeEach(func() {
			mockCredentials = &BindingCredentials{
				Host:            "redis.example.com",
				Port:            6379,
				Username:        "reconstructed-user",
				Password:        "reconstructed-pass",
				CredentialType:  "dynamic",
				ReconstructedAt: time.Now().Format(time.RFC3339),
				Raw: map[string]interface{}{
					"host":     "redis.example.com",
					"port":     6379,
					"username": "reconstructed-user",
					"password": "reconstructed-pass",
				},
			}

			mockBroker.SetCredentials(instanceID, bindingID, mockCredentials)
		})

		It("should successfully reconstruct binding credentials", func() {
			err := updater.ReconstructBindingWithBroker(instanceID, bindingID, mockBroker)
			Expect(err).ToNot(HaveOccurred())

			// Verify credentials were stored
			storedData := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/credentials")
			Expect(storedData).To(HaveKey("host"))
			Expect(storedData["host"]).To(Equal("redis.example.com"))
			Expect(storedData["username"]).To(Equal("reconstructed-user"))
		})

		It("should store reconstruction metadata", func() {
			err := updater.ReconstructBindingWithBroker(instanceID, bindingID, mockBroker)
			Expect(err).ToNot(HaveOccurred())

			// Verify metadata was stored
			metadataData := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/metadata")
			Expect(metadataData).To(HaveKey("reconstruction_source"))
			Expect(metadataData["reconstruction_source"]).To(Equal("broker"))
			Expect(metadataData["status"]).To(Equal("reconstructed"))
		})

		Context("when broker reconstruction fails", func() {
			BeforeEach(func() {
				mockBroker.SetError(instanceID, bindingID, errors.New("broker reconstruction failed"))
			})

			It("should return an error", func() {
				err := updater.ReconstructBindingWithBroker(instanceID, bindingID, mockBroker)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("broker reconstruction failed"))
			})
		})
	})

	Describe("RepairInstanceBindings", func() {
		Context("when no unhealthy bindings exist", func() {
			BeforeEach(func() {
				// Set up healthy binding
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})

				mockVault.SetData(instanceID+"/bindings/"+bindingID+"/credentials", map[string]interface{}{
					"host":     "redis.example.com",
					"port":     6379,
					"username": "redis-user",
					"password": "redis-pass",
				})
			})

			It("should complete without errors", func() {
				err := updater.RepairInstanceBindings(instanceID, mockBroker)
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when unhealthy bindings exist", func() {
			BeforeEach(func() {
				// Set up unhealthy binding
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})

				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", errors.New("not found"))

				// Set up broker to successfully reconstruct
				mockBroker.SetCredentials(instanceID, bindingID, &BindingCredentials{
					Host:           "redis.example.com",
					Port:           6379,
					Username:       "repaired-user",
					Password:       "repaired-pass",
					CredentialType: "dynamic",
				})
			})

			It("should repair all unhealthy bindings", func() {
				err := updater.RepairInstanceBindings(instanceID, mockBroker)
				Expect(err).ToNot(HaveOccurred())

				// Verify credentials were stored
				storedData := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/credentials")
				Expect(storedData).To(HaveKey("username"))
				Expect(storedData["username"]).To(Equal("repaired-user"))
			})
		})

		Context("when some bindings fail to repair", func() {
			var failingBindingID string

			BeforeEach(func() {
				failingBindingID = "failing-binding-22222222-2222-2222-2222-222222222222"

				// Set up two unhealthy bindings
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
					failingBindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})

				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", errors.New("not found"))
				mockVault.SetError(instanceID+"/bindings/"+failingBindingID+"/credentials", errors.New("not found"))

				// Set up broker to succeed for one, fail for another
				mockBroker.SetCredentials(instanceID, bindingID, &BindingCredentials{
					Host:     "redis.example.com",
					Port:     6379,
					Username: "repaired-user",
					Password: "repaired-pass",
				})
				mockBroker.SetError(instanceID, failingBindingID, errors.New("reconstruction failed"))
			})

			It("should return error but repair what it can", func() {
				err := updater.RepairInstanceBindings(instanceID, mockBroker)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to repair 1 of 2 bindings"))

				// Verify one binding was repaired
				repairedData := mockVault.GetData(instanceID + "/bindings/" + bindingID + "/credentials")
				Expect(repairedData).To(HaveKey("username"))
			})
		})
	})

	Describe("UpdateInstanceWithBindingRepair", func() {
		var instance *InstanceData

		BeforeEach(func() {
			instance = &InstanceData{
				ID:        instanceID,
				ServiceID: "redis-service",
				PlanID:    "small",
				Deployment: DeploymentDetail{
					DeploymentInfo: DeploymentInfo{
						Name: "redis-small-" + instanceID,
					},
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
				Metadata:  make(map[string]interface{}),
			}
		})

		Context("when no binding repair is needed", func() {
			BeforeEach(func() {
				instance.Metadata["needs_binding_repair"] = false
			})

			It("should perform normal update only", func() {
				err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, mockBroker)
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when binding repair is needed and succeeds", func() {
			BeforeEach(func() {
				instance.Metadata["needs_binding_repair"] = true

				// Set up successful repair scenario
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})
				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", errors.New("not found"))

				mockBroker.SetCredentials(instanceID, bindingID, &BindingCredentials{
					Host:     "redis.example.com",
					Port:     6379,
					Username: "repaired-user",
					Password: "repaired-pass",
				})
			})

			It("should repair bindings and update metadata", func() {
				err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, mockBroker)
				Expect(err).ToNot(HaveOccurred())
				Expect(instance.Metadata["needs_binding_repair"]).To(BeFalse())
				Expect(instance.Metadata["binding_repair_succeeded"]).To(BeTrue())
			})
		})

		Context("when binding repair is needed but fails", func() {
			BeforeEach(func() {
				instance.Metadata["needs_binding_repair"] = true

				// Set up failed repair scenario
				mockVault.SetData(instanceID+"/bindings", map[string]interface{}{
					bindingID: map[string]interface{}{
						"service_id": "redis-service",
						"plan_id":    "small",
					},
				})
				mockVault.SetError(instanceID+"/bindings/"+bindingID+"/credentials", errors.New("not found"))
				mockBroker.SetError(instanceID, bindingID, errors.New("repair failed"))
			})

			It("should update metadata with failure information but not fail overall", func() {
				err := updater.UpdateInstanceWithBindingRepair(context.Background(), instance, mockBroker)
				Expect(err).ToNot(HaveOccurred()) // Should not fail overall
				Expect(instance.Metadata["binding_repair_failed"]).To(BeTrue())
				Expect(instance.Metadata["binding_repair_error"]).To(ContainSubstring("repair failed"))
			})
		})
	})
})

// Mock implementations for testing

type MockVaultUpdater struct {
	data   map[string]map[string]interface{}
	errors map[string]error
}

func NewMockVaultUpdater() *MockVaultUpdater {
	return &MockVaultUpdater{
		data:   make(map[string]map[string]interface{}),
		errors: make(map[string]error),
	}
}

func (mv *MockVaultUpdater) SetData(path string, data map[string]interface{}) {
	mv.data[path] = data
}

func (mv *MockVaultUpdater) GetData(path string) map[string]interface{} {
	return mv.data[path]
}

func (mv *MockVaultUpdater) SetError(path string, err error) {
	mv.errors[path] = err
}

type MockBroker struct {
	credentials map[string]*BindingCredentials
	errors      map[string]error
}

func NewMockBroker() *MockBroker {
	return &MockBroker{
		credentials: make(map[string]*BindingCredentials),
		errors:      make(map[string]error),
	}
}

func (mb *MockBroker) SetCredentials(instanceID, bindingID string, creds *BindingCredentials) {
	key := instanceID + "/" + bindingID
	mb.credentials[key] = creds
}

func (mb *MockBroker) SetError(instanceID, bindingID string, err error) {
	key := instanceID + "/" + bindingID
	mb.errors[key] = err
}

func (mb *MockBroker) GetBindingCredentials(instanceID, bindingID string) (*BindingCredentials, error) {
	key := instanceID + "/" + bindingID

	if err, exists := mb.errors[key]; exists {
		return nil, err
	}

	creds, exists := mb.credentials[key]
	if !exists {
		return nil, errors.New("credentials not found")
	}

	return creds, nil
}

func (mb *MockBroker) GetServices() []Service {
	return []Service{}
}

type MockLogger struct{}

func NewMockLogger() *MockLogger {
	return &MockLogger{}
}

func (ml *MockLogger) Debug(format string, args ...interface{})   {}
func (ml *MockLogger) Info(format string, args ...interface{})    {}
func (ml *MockLogger) Error(format string, args ...interface{})   {}
func (ml *MockLogger) Warning(format string, args ...interface{}) {}

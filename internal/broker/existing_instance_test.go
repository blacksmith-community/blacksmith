package broker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/broker"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"

	osbapibroker "github.com/fivetwenty-io/osbapi/v2/pkg/broker"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// A platform that repeats a provision for an instance the broker already
// knows must get an answer from the broker's records, and the broker must
// never start a second deploy over the instance. The specs build a fresh
// broker for the repeat, the way a restart does, so no operation claim from
// the first provision is held.
var _ = Describe("A provision for an instance the index already has", func() {
	const (
		serviceID   = "valkey"
		planID      = "standalone"
		otherPlanID = "cluster"
		taskID      = 77
	)

	var (
		ctx            context.Context
		stopService    context.CancelFunc
		director       *testutil.ScriptedBOSHDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		plans          map[string]services.Plan
		instanceID     string
		deploymentName string
		request        osbapi.ProvisionRequest
		dir            string
	)

	newBroker := func(vault *internalVault.Vault) *broker.Broker {
		serviceCtx, cancel := context.WithCancel(context.Background())

		previous := stopService
		stopService = func() {
			cancel()

			if previous != nil {
				previous()
			}
		}

		return &broker.Broker{
			Plans:          plans,
			BOSH:           director,
			Vault:          vault,
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		stopService = nil
		instanceID = fmt.Sprintf("existing-%d", time.Now().UnixNano())
		deploymentName = planID + "-" + instanceID

		// The request carries numbers as float64, the type a JSON decode
		// gives them, as it does when it arrives through the OSB handler.
		request = osbapi.ProvisionRequest{
			ServiceID:        serviceID,
			PlanID:           planID,
			OrganizationGUID: "org-guid",
			SpaceGUID:        "space-guid",
			Parameters:       map[string]any{"memory": "small", "replicas": float64(3)},
		}

		director = testutil.NewScriptedBOSHDirector()
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
		director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }

		var err error

		dir, err = os.MkdirTemp("", "blacksmith-existing")
		Expect(err).ToNot(HaveOccurred())

		initScript := filepath.Join(dir, "init")
		Expect(os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		plans = map[string]services.Plan{}

		for _, id := range []string{planID, otherPlanID} {
			plans[serviceID+"/"+id] = services.Plan{
				ID:             id,
				Name:           id,
				Type:           "valkey",
				InitScriptPath: initScript,
				Manifest: map[interface{}]interface{}{
					"name":            "placeholder",
					"instance_groups": []interface{}{},
				},
				Service: &services.Service{ID: serviceID, Name: serviceID},
			}
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance = newBroker(vaultClient)
	})

	AfterEach(func() {
		stopBrokerGoroutines(stopService)

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	// seedAcceptedProvision writes the records an accepted provision leaves,
	// through the broker's own writers, and the created_at its completion
	// hook adds when finished is true.
	seedAcceptedProvision := func(seeded osbapi.ProvisionRequest, finished bool) {
		seeder := newBroker(vaultClient)
		plan := plans[seeded.ServiceID+"/"+seeded.PlanID]

		Expect(seeder.RecordInitialRequest(ctx, instanceID, seeded)).To(Succeed())
		Expect(seeder.UpdateInstanceStatus(ctx, instanceID, seeded, &plan, "provisioning_started")).To(Succeed())
		Expect(seeder.StoreDeploymentDetails(ctx, instanceID, deploymentName, seeded)).To(Succeed())
		seeder.StoreDeploymentNameInIndex(ctx, instanceID, deploymentName)
		seeder.RecordTask(ctx, instanceID, "provision", fmt.Sprintf("BOSH deployment in progress (task %d)", taskID), taskID)

		if finished {
			Expect(seeder.UpdateInstanceTimestamp(ctx, instanceID)).To(Succeed())
		}
	}

	indexEntry := func() map[string]interface{} {
		index, err := suite.vault.ReadSecret("db")
		Expect(err).ToNot(HaveOccurred())

		entry, ok := index[instanceID].(map[string]interface{})
		Expect(ok).To(BeTrue(), "instance %s has no index entry", instanceID)

		return entry
	}

	taskRecord := func() map[string]interface{} {
		record, err := suite.vault.ReadSecret(instanceID + "/task")
		Expect(err).ToNot(HaveOccurred())

		return record
	}

	// expectNothingDeployed checks that the repeat neither rewrote the
	// instance's records nor reached the director.
	expectNothingDeployed := func(entryBefore, taskBefore map[string]interface{}) {
		Expect(indexEntry()).To(Equal(entryBefore), "the repeat rewrote the index entry")
		Expect(taskRecord()).To(Equal(taskBefore), "the repeat rewrote the task record")
		Consistently(func() []string { return director.Calls("CreateDeployment") }, 500*time.Millisecond).Should(BeEmpty(), "the repeat started a deploy")
	}

	It("a repeat of a finished provision answers success at once and deploys nothing", func() {
		seedAcceptedProvision(request, true)

		entryBefore, taskBefore := indexEntry(), taskRecord()

		response, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeFalse(), "a finished instance was answered as an operation in progress")
		Expect(response).To(Equal(osbapi.ProvisionResponse{}), "the answer carried an operation or a dashboard URL")

		expectNothingDeployed(entryBefore, taskBefore)
	})

	// The OSB library answers a provision that returns no operation with 201
	// Created, its only synchronous success. The platform treats it as it
	// treats 200 OK, and the instance is not deployed again.
	It("a repeat of a finished provision answers 201 through the OSB handler", func() {
		seedAcceptedProvision(request, true)

		server := osbHandlerServer(brokerInstance)
		defer server.Close()

		status, body := sendOSBRequest(server.URL, http.MethodPut, "/v2/service_instances/"+instanceID+"?accepts_incomplete=true", request)
		Expect(status).To(Equal(http.StatusCreated), "body: %s", body)
		Expect(body).ToNot(ContainSubstring("dashboard_url"))
		Expect(director.Calls("CreateDeployment")).To(BeEmpty())
	})

	differences := []struct {
		name   string
		change func(*osbapi.ProvisionRequest)
	}{
		{"a repeat with different parameters answers 409", func(r *osbapi.ProvisionRequest) { r.Parameters["memory"] = "large" }},
		{"a repeat with no parameters answers 409", func(r *osbapi.ProvisionRequest) { r.Parameters = nil }},
		{"a repeat with a different plan answers 409", func(r *osbapi.ProvisionRequest) { r.PlanID = otherPlanID }},
		{"a repeat with a different organization answers 409", func(r *osbapi.ProvisionRequest) { r.OrganizationGUID = "other-org" }},
		{"a repeat with a different space answers 409", func(r *osbapi.ProvisionRequest) { r.SpaceGUID = "other-space" }},
	}

	for _, difference := range differences {
		It(difference.name, func() {
			seedAcceptedProvision(request, true)

			entryBefore, taskBefore := indexEntry(), taskRecord()

			different := request
			different.Parameters = map[string]any{"memory": "small", "replicas": float64(3)}
			difference.change(&different)

			_, async, err := brokerInstance.Provision(ctx, instanceID, different, true)
			Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(BeTrue(), "the different request was not refused as a conflict: async=%t err=%v", async, err)
			Expect(async).To(BeFalse())

			expectNothingDeployed(entryBefore, taskBefore)
		})
	}

	It("a repeat while the provision runs after a restart answers 202", func() {
		seedAcceptedProvision(request, false)

		entryBefore, taskBefore := indexEntry(), taskRecord()

		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeFalse(), "the spec needs a broker that holds no claim")

		response, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
		Expect(response).To(Equal(osbapi.ProvisionResponse{Operation: "provision"}))

		expectNothingDeployed(entryBefore, taskBefore)
	})

	It("compares parameters in the form the stored details take", func() {
		typed := request
		typed.Parameters = map[string]any{
			"memory":   "small",
			"replicas": 3,
			"limits":   map[string]int{"connections": 100},
		}

		seedAcceptedProvision(typed, false)

		entryBefore, taskBefore := indexEntry(), taskRecord()

		_, async, err := brokerInstance.Provision(ctx, instanceID, typed, true)
		Expect(err).ToNot(HaveOccurred(), "a request whose values only differ in Go type from the stored JSON was refused")
		Expect(async).To(BeTrue())

		expectNothingDeployed(entryBefore, taskBefore)
	})

	It("treats omitted parameters and empty ones alike", func() {
		withoutParameters := request
		withoutParameters.Parameters = nil

		seedAcceptedProvision(withoutParameters, false)

		empty := request
		empty.Parameters = map[string]any{}

		_, async, err := brokerInstance.Provision(ctx, instanceID, empty, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
	})

	endings := []struct {
		name     string
		finished bool
		mark     func()
	}{
		{"a repeat for a failed provision answers 409", false, func() {
			brokerInstance.RecordTask(ctx, instanceID, "provision", "BOSH deployment task 77 is error", -1)
		}},
		{"a repeat for an instance the vm-monitor marked deleted answers 409", true, func() {
			Expect(vaultClient.MarkInstanceDeleted(ctx, instanceID)).To(Succeed())
		}},
		{"a repeat for an instance with the older deleted flag answers 409", true, func() {
			Expect(vaultClient.UpdateIndexEntry(ctx, instanceID, map[string]interface{}{"deleted": true})).To(Succeed())
		}},
		{"a repeat for an instance being deprovisioned answers 409", true, func() {
			brokerInstance.RecordProgress(ctx, instanceID, "deprovision", "Deprovision request accepted")
		}},
	}

	for _, ending := range endings {
		It(ending.name, func() {
			seedAcceptedProvision(request, ending.finished)
			ending.mark()

			entryBefore, taskBefore := indexEntry(), taskRecord()

			_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
			Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(BeTrue(), "the repeat was not refused as a conflict: async=%t err=%v", async, err)
			Expect(async).To(BeFalse())

			expectNothingDeployed(entryBefore, taskBefore)
		})
	}

	It("a provision whose index read fails deploys nothing", func() {
		seedAcceptedProvision(request, true)

		entryBefore, taskBefore := indexEntry(), taskRecord()

		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		brokerInstance = newBroker(internalVault.New(proxy.Addr, suite.vault.RootToken, true))

		// Only the first read of the index fails, so a broker that takes the
		// failure for a missing entry finds the index readable again when it
		// goes on to write.
		proxy.FailReads("secret/data/db", http.StatusForbidden, "permission denied", 1)

		_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(err).To(HaveOccurred())
		Expect(async).To(BeFalse())
		Expect(proxy.WritesTo("/db")).To(BeEmpty(), "the broker wrote the index after it failed to read it")
		Expect(brokerInstance.ProvisionActive(instanceID)).To(BeFalse(), "the refused provision left its claim behind")

		expectNothingDeployed(entryBefore, taskBefore)
	})

	// An entry that the accept chain left before it accepted the provision,
	// such as one refused for the plan's limit, records no running or
	// finished instance. A retry runs the accept chain as a new provision.
	// An accept chain that refused its request can leave a partial entry
	// behind, and so can a delete of an instance the broker never had. None
	// of them records an operation, so a retry is provisioned as new.
	partials := []struct {
		name string
		seed func()
	}{
		{"a retry after the accept chain refused the request is provisioned as new", func() {
			Expect(brokerInstance.RecordInitialRequest(ctx, instanceID, request)).To(Succeed())
		}},
		{"a retry after the accept chain failed to record the provision is provisioned as new", func() {
			plan := plans[serviceID+"/"+planID]

			Expect(brokerInstance.RecordInitialRequest(ctx, instanceID, request)).To(Succeed())
			Expect(brokerInstance.UpdateInstanceStatus(ctx, instanceID, request, &plan, "provisioning_started")).To(Succeed())
		}},
		{"a provision after a refused delete of an unknown instance is provisioned as new", func() {
			deprovision := osbapi.DeprovisionRequest{ServiceID: serviceID, PlanID: planID}

			Expect(brokerInstance.RecordDeprovisionRequest(ctx, instanceID, deprovision)).To(Succeed())
		}},
	}

	for _, partial := range partials {
		It(partial.name, func() {
			partial.seed()

			response, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(async).To(BeTrue())
			Expect(response).To(Equal(osbapi.ProvisionResponse{Operation: "provision"}))

			Eventually(func() []string { return director.Calls("CreateDeployment") }, 10*time.Second).ShouldNot(BeEmpty())
		})
	}
})

// osbHandlerServer serves b through the OSB library's HTTP handler on a
// loopback address.
func osbHandlerServer(b *broker.Broker) *httptest.Server {
	return httptest.NewServer(osbapibroker.NewHandler(b))
}

// sendOSBRequest sends an OSB API request with a JSON body, when body is not
// nil, and returns the status and the body of the answer.
func sendOSBRequest(baseURL, method, path string, body interface{}) (int, string) {
	var reader io.Reader

	if body != nil {
		encoded, err := json.Marshal(body)
		Expect(err).ToNot(HaveOccurred())

		reader = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, reader)
	Expect(err).ToNot(HaveOccurred())

	request.Header.Set(osbapi.HeaderAPIVersion, "2.17")
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	Expect(err).ToNot(HaveOccurred())

	defer func() { _ = response.Body.Close() }()

	answer, err := io.ReadAll(response.Body)
	Expect(err).ToNot(HaveOccurred())

	return response.StatusCode, strings.TrimSpace(string(answer))
}

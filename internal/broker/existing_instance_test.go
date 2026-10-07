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
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
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
		// The deprovision record alone shows that the instance exists, because
		// a provision that never finished leaves no created_at behind.
		{"a repeat for an unfinished instance being deprovisioned answers 409", false, func() {
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

	// A record of an existing instance that cannot be read must fail the
	// repeat. Taking it for a missing record would provision the instance as
	// new and deploy over it.
	unreadable := []struct {
		name string
		path func() string
	}{
		{"a repeat whose task record cannot be read deploys nothing", func() string { return instanceID + "/task" }},
		{"a repeat whose stored details cannot be read deploys nothing", func() string { return instanceID + "/" + deploymentName }},
	}

	for _, record := range unreadable {
		It(record.name, func() {
			seedAcceptedProvision(request, true)

			entryBefore, taskBefore := indexEntry(), taskRecord()

			proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
			Expect(err).ToNot(HaveOccurred())

			defer proxy.Close()

			brokerInstance = newBroker(internalVault.New(proxy.Addr, suite.vault.RootToken, true))

			// Only the first read fails, so a broker that takes the failure for
			// a missing record finds every record readable when it goes on.
			proxy.FailReads(record.path(), http.StatusForbidden, "permission denied", 1)

			_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(BeFalse(), "a read failure was answered as a conflict: %v", err)
			Expect(async).To(BeFalse())

			expectNothingDeployed(entryBefore, taskBefore)
		})
	}

	// The created_at that the completion hook writes shows a finished
	// provision, even when the task record that would show it is gone.
	It("a repeat of a finished provision whose task record is gone answers 201 and deploys nothing", func() {
		seedAcceptedProvision(request, true)
		Expect(vaultClient.Delete(ctx, instanceID+"/task")).To(Succeed())

		entryBefore := indexEntry()

		server := osbHandlerServer(brokerInstance)
		defer server.Close()

		status, body := sendOSBRequest(server.URL, http.MethodPut, "/v2/service_instances/"+instanceID+"?accepts_incomplete=true", request)
		Expect(status).To(Equal(http.StatusCreated), "body: %s", body)

		Expect(indexEntry()).To(Equal(entryBefore), "the repeat rewrote the index entry")
		Consistently(func() []string { return director.Calls("CreateDeployment") }, 500*time.Millisecond).Should(BeEmpty(), "the repeat started a deploy")

		_, err := suite.vault.ReadSecret(instanceID + "/task")
		Expect(err).To(MatchError(testutil.ErrSecretNotFound), "the repeat wrote a task record")
	})

	// Without the details stored at provision, the request cannot be shown
	// to match the instance, so it is refused rather than taken for a match.
	It("a repeat whose stored details are missing answers 409", func() {
		seedAcceptedProvision(request, true)
		Expect(vaultClient.Delete(ctx, instanceID+"/"+deploymentName)).To(Succeed())

		entryBefore, taskBefore := indexEntry(), taskRecord()

		_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
		Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(BeTrue(), "the repeat was not refused as a conflict: async=%t err=%v", async, err)
		Expect(async).To(BeFalse())

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

			director.GetDeploymentFn = deploymentNotFound

			response, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(async).To(BeTrue())
			Expect(response).To(Equal(osbapi.ProvisionResponse{Operation: "provision"}))
			Expect(director.Calls("GetDeployment")).To(ContainElement(deploymentName), "the director was not asked for the deployment the provision creates")

			Eventually(func() []string { return director.Calls("CreateDeployment") }, 10*time.Second).ShouldNot(BeEmpty())
		})
	}

	// A partial entry can sit beside a deployment that exists, such as one
	// the reconciler adopted with no task record and no created_at. Only the
	// director's deployment-not-found answer lets the provision deploy.
	directorAnswers := []struct {
		name      string
		answer    func(name string) (*bosh.DeploymentDetail, error)
		conflicts bool
	}{
		{"a provision over a partial entry whose deployment exists answers 409 and deploys nothing", func(name string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: name}, nil
		}, true},
		{"a provision over a partial entry fails and deploys nothing when the director cannot say whether its deployment exists", func(string) (*bosh.DeploymentDetail, error) {
			return nil, errDirectorUnreachable
		}, false},
	}

	for _, directorAnswer := range directorAnswers {
		It(directorAnswer.name, func() {
			Expect(brokerInstance.RecordInitialRequest(ctx, instanceID, request)).To(Succeed())

			director.GetDeploymentFn = directorAnswer.answer
			entryBefore := indexEntry()

			_, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(Equal(directorAnswer.conflicts), "the provision answered %v", err)
			Expect(async).To(BeFalse())
			Expect(director.Calls("GetDeployment")).To(ContainElement(deploymentName))

			Expect(indexEntry()).To(Equal(entryBefore), "the provision rewrote the index entry")
			Consistently(func() []string { return director.Calls("CreateDeployment") }, 500*time.Millisecond).Should(BeEmpty(), "the provision started a deploy")

			_, err = suite.vault.ReadSecret(instanceID + "/task")
			Expect(err).To(MatchError(testutil.ErrSecretNotFound), "the provision wrote a task record")
		})
	}

	// A partial entry can record the name of a deployment the reconciler
	// adopted under another plan. The provision would create a second
	// deployment beside it, so the director is asked for that name as well,
	// and only its deployment-not-found answer for both names lets the
	// provision deploy.
	recordedAnswers := []struct {
		name     string
		answer   func(name string) (*bosh.DeploymentDetail, error)
		deploys  bool
		conflict bool
	}{
		{"a provision over an entry with a recorded deployment name asks the director for that name", func(name string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: name}, nil
		}, false, true},
		{"a provision over an entry whose recorded deployment cannot be checked fails", func(string) (*bosh.DeploymentDetail, error) {
			return nil, errDirectorUnreachable
		}, false, false},
		{"a provision over an entry whose recorded deployment is gone is provisioned as new", deploymentNotFound, true, false},
	}

	for _, recordedAnswer := range recordedAnswers {
		It(recordedAnswer.name, func() {
			recordedName := otherPlanID + "-" + instanceID

			Expect(brokerInstance.RecordInitialRequest(ctx, instanceID, request)).To(Succeed())
			Expect(vaultClient.UpdateIndexEntry(ctx, instanceID, map[string]interface{}{"deployment_name": recordedName})).To(Succeed())

			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				if name == recordedName {
					return recordedAnswer.answer(name)
				}

				return deploymentNotFound(name)
			}

			entryBefore := indexEntry()

			response, async, err := brokerInstance.Provision(ctx, instanceID, request, true)
			Expect(director.Calls("GetDeployment")).To(ContainElement(recordedName), "the director was not asked for the recorded deployment")

			if recordedAnswer.deploys {
				Expect(err).ToNot(HaveOccurred())
				Expect(async).To(BeTrue())
				Expect(response).To(Equal(osbapi.ProvisionResponse{Operation: "provision"}))
				Expect(director.Calls("GetDeployment")).To(ContainElement(deploymentName), "the director was not asked for the deployment the provision creates")

				Eventually(func() []string { return director.Calls("CreateDeployment") }, 10*time.Second).ShouldNot(BeEmpty())

				return
			}

			Expect(err).To(HaveOccurred(), "the provision went ahead")
			Expect(errors.Is(err, osbapi.ErrInstanceAlreadyExists)).To(Equal(recordedAnswer.conflict), "the provision answered %v", err)
			Expect(async).To(BeFalse())

			Expect(indexEntry()).To(Equal(entryBefore), "the provision rewrote the index entry")
			Consistently(func() []string { return director.Calls("CreateDeployment") }, 500*time.Millisecond).Should(BeEmpty(), "the provision started a deploy")

			_, err = suite.vault.ReadSecret(instanceID + "/task")
			Expect(err).To(MatchError(testutil.ErrSecretNotFound), "the provision wrote a task record")
		})
	}
})

// deploymentNotFound answers a GetDeployment the way the director adapter
// does when the director answers with its deployment-not-found code.
func deploymentNotFound(name string) (*bosh.DeploymentDetail, error) {
	return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
}

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

// A poll for an instance the index no longer has must not tell the platform
// a provision succeeded. Only the records that outlive the index entry, the
// task record and the metadata, can show that the instance was deleted.
var _ = Describe("A poll for an instance the index does not have", func() {
	var (
		ctx            context.Context
		stopService    context.CancelFunc
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("unindexed-%d", time.Now().UnixNano())

		var serviceCtx context.Context

		serviceCtx, stopService = context.WithCancel(context.Background())
		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance = &broker.Broker{
			Plans:          map[string]services.Plan{},
			BOSH:           testutil.NewScriptedBOSHDirector(),
			Vault:          vaultClient,
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}
	})

	AfterEach(func() {
		stopBrokerGoroutines(stopService)
	})

	writeMetadata := func(metadata map[string]interface{}) {
		Expect(vaultClient.Put(ctx, instanceID+"/metadata", metadata)).To(Succeed())
	}

	poll := func(operation string) osbapi.LastOperationResponse {
		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: operation})
		Expect(err).ToNot(HaveOccurred())

		return response
	}

	It("a provision poll with no entry and no delete record answers failed", func() {
		brokerInstance.RecordTask(ctx, instanceID, "provision", "BOSH deployment in progress (task 77)", 77)
		writeMetadata(map[string]interface{}{"provision_requested_at": time.Now().UTC().Format(time.RFC3339)})

		for _, operation := range []string{"provision", ""} {
			response := poll(operation)
			Expect(response.State).To(Equal(osbapi.StateFailed), "a poll with operation %q answered %+v", operation, response)
			Expect(response.Description).To(ContainSubstring("no record of service instance " + instanceID))
			Expect(response.Description).To(ContainSubstring("Delete the service instance"))
		}
	})

	It("a poll for an instance with no records at all answers failed", func() {
		Expect(poll("provision").State).To(Equal(osbapi.StateFailed))
	})

	It("a deprovision poll after the entry is removed still answers succeeded", func() {
		brokerInstance.RecordTask(ctx, instanceID, "deprovision", "BOSH delete in progress (task 78)", 78)

		for _, operation := range []string{"deprovision", ""} {
			response := poll(operation)
			Expect(response.State).To(Equal(osbapi.StateSucceeded), "a poll with operation %q answered %+v", operation, response)
		}
	})

	It("a poll after a delete request on record answers succeeded", func() {
		created := time.Now().UTC().Add(-time.Hour)

		brokerInstance.RecordTask(ctx, instanceID, "provision", "BOSH deployment in progress (task 77)", 77)
		writeMetadata(map[string]interface{}{
			"provision_requested_at": created.Format(time.RFC3339),
			"created_at":             created.Format(time.RFC3339),
			"delete_requested_at":    time.Now().UTC().Format(time.RFC3339),
		})

		Expect(poll("").State).To(Equal(osbapi.StateSucceeded))
	})

	It("a delete request from an earlier instance with the same ID does not count", func() {
		earlier := time.Now().UTC().Add(-time.Hour)

		brokerInstance.RecordTask(ctx, instanceID, "provision", "BOSH deployment in progress (task 77)", 77)
		writeMetadata(map[string]interface{}{
			"delete_requested_at":    earlier.Format(time.RFC3339),
			"provision_requested_at": time.Now().UTC().Format(time.RFC3339),
		})

		Expect(poll("provision").State).To(Equal(osbapi.StateFailed))
	})

	It("a provision poll while this process holds the provision claim answers in progress", func() {
		release := brokerInstance.HoldOperationClaim(instanceID, "provision")
		defer release()

		Expect(poll("provision").State).To(Equal(osbapi.StateInProgress))
	})

	It("a poll whose task record read fails returns an error", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)
		proxy.FailReads(instanceID+"/task", http.StatusForbidden, "permission denied", 1)

		_, err = brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: "provision"})
		Expect(err).To(HaveOccurred())
	})

	It("a poll whose metadata read fails returns an error", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		brokerInstance.RecordTask(ctx, instanceID, "provision", "BOSH deployment in progress (task 77)", 77)
		brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)
		proxy.FailReads(instanceID+"/metadata", http.StatusForbidden, "permission denied", 1)

		_, err = brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{Operation: "provision"})
		Expect(err).To(HaveOccurred())
	})
})

// A delete of an instance the index does not have answers 410 Gone when the
// director confirms, with its own deployment-not-found code, that the
// deployment named from the request's plan and the instance ID is gone. Any
// other director answer leaves the instance's existence unknown, so the
// delete fails and the platform retries it. A plain test, because the
// director is a TLS test server driven through the real director adapter.
//
//nolint:paralleltest // t.Setenv cannot be used with t.Parallel
func TestDeleteOfUnknownInstance(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	vault, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	defer vault.Close()

	cases := []struct {
		name       string
		answer     func(http.ResponseWriter, *http.Request)
		wantStatus int
	}{
		{"deleting an unknown instance answers 410", func(writer http.ResponseWriter, _ *http.Request) {
			boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment doesn't exist")
		}, http.StatusGone},
		{"a 404 without the director's code is not taken for a missing deployment", boshtest.WriteRouteNotFound, http.StatusInternalServerError},
		{"a director error is not taken for a missing deployment", func(writer http.ResponseWriter, _ *http.Request) {
			boshtest.WriteText(writer, http.StatusInternalServerError, "director failure")
		}, http.StatusInternalServerError},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			instanceID := fmt.Sprintf("unknown-%d", time.Now().UnixNano())
			deploymentPath := "/deployments/standalone-" + instanceID

			director := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
				if request.URL.Path != deploymentPath {
					return false
				}

				if request.Method == http.MethodGet {
					testCase.answer(writer, request)
				} else {
					boshtest.WriteText(writer, http.StatusInternalServerError, "the broker must not delete the deployment")
				}

				return true
			})

			adapter, err := bosh.NewDirectorAdapter(bosh.Config{
				Address:  director.URL,
				Username: "admin",
				Password: "admin",
				CACert:   director.CACert,
			})
			if err != nil {
				t.Fatalf("failed to create the director adapter: %v", err)
			}

			serviceCtx, stopService := context.WithCancel(context.Background())
			defer waitForBrokerGoroutines(t, stopService)

			vaultClient := internalVault.New(vault.Addr, vault.RootToken, true)
			brokerInstance := &broker.Broker{
				Plans:          map[string]services.Plan{},
				BOSH:           adapter,
				Vault:          vaultClient,
				Shield:         &shield.NoopClient{},
				Config:         &config.Config{},
				InstanceLocks:  make(map[string]*sync.Mutex),
				ServiceContext: serviceCtx,
			}

			server := httptest.NewServer(osbapibroker.NewHandler(brokerInstance))
			defer server.Close()

			status, body := deleteThroughOSB(t, server.URL, instanceID)
			if status != testCase.wantStatus {
				t.Fatalf("the delete answered %d, want %d: %s", status, testCase.wantStatus, body)
			}

			var deletes []string

			for _, request := range director.Requests() {
				if request.Path == deploymentPath && request.Method != http.MethodGet {
					deletes = append(deletes, request.Method)
				}
			}

			if len(deletes) != 0 {
				t.Fatalf("the broker sent %v for the deployment", deletes)
			}

			_, indexed, err := vaultClient.FindInstance(context.Background(), instanceID)
			if err != nil || indexed {
				t.Fatalf("the delete left an index entry (indexed %t, err %v)", indexed, err)
			}

			for _, path := range []string{"/metadata", "/task"} {
				var record map[string]interface{}

				exists, err := vaultClient.Get(context.Background(), instanceID+path, &record)
				if err != nil || exists {
					t.Fatalf("the delete wrote %s%s (exists %t, err %v)", instanceID, path, exists, err)
				}
			}
		})
	}
}

// deleteThroughOSB sends a deprovision for instanceID on the standalone plan
// through the OSB handler at baseURL and returns the answer's status and body.
func deleteThroughOSB(t *testing.T, baseURL, instanceID string) (int, string) {
	t.Helper()

	path := "/v2/service_instances/" + instanceID + "?service_id=valkey&plan_id=standalone&accepts_incomplete=true"

	request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, baseURL+path, nil)
	if err != nil {
		t.Fatalf("failed to build the delete: %v", err)
	}

	request.Header.Set(osbapi.HeaderAPIVersion, "2.17")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("failed to send the delete: %v", err)
	}

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("failed to read the answer: %v", err)
	}

	return response.StatusCode, string(body)
}

// waitForBrokerGoroutines stops the broker's service context and waits for
// every goroutine of package broker to return, so none outlives the test.
func waitForBrokerGoroutines(t *testing.T, stopService context.CancelFunc) {
	t.Helper()

	stopService()

	deadline := time.Now().Add(backgroundStopTimeout)

	for len(broker.BackgroundGoroutines()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines of package broker outlived the test: %v", broker.BackgroundGoroutines())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// A delete of an instance the index does not have goes ahead when the
// director still has the deployment named from the request's plan and the
// instance ID, because only the delete can remove it.
var _ = Describe("A delete of an instance the index does not have", func() {
	var (
		ctx            context.Context
		stopService    context.CancelFunc
		director       *testutil.ScriptedBOSHDirector
		brokerInstance *broker.Broker
		instanceID     string
		deploymentName string
		request        osbapi.DeprovisionRequest
	)

	newBroker := func(vault *internalVault.Vault) *broker.Broker {
		var serviceCtx context.Context

		serviceCtx, stopService = context.WithCancel(context.Background())

		return &broker.Broker{
			Plans:          map[string]services.Plan{},
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
		instanceID = fmt.Sprintf("unindexed-delete-%d", time.Now().UnixNano())
		deploymentName = "standalone-" + instanceID
		request = osbapi.DeprovisionRequest{ServiceID: "valkey", PlanID: "standalone"}

		director = testutil.NewScriptedBOSHDirector()
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) { return &bosh.Task{ID: 91, State: "queued"}, nil }
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) { return &bosh.Task{ID: taskID, State: "processing"}, nil }
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: name}, nil
		}

		brokerInstance = newBroker(internalVault.New(suite.vault.Addr, suite.vault.RootToken, true))
	})

	AfterEach(func() {
		stopBrokerGoroutines(stopService)

		_ = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true).Index(ctx, instanceID, nil)
	})

	It("deletes a deployment the director still has", func() {
		response, async, err := brokerInstance.Deprovision(ctx, instanceID, request, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())
		Expect(response).To(Equal(osbapi.DeprovisionResponse{Operation: "deprovision"}))

		Expect(director.Calls("GetDeployment")).To(ContainElement(deploymentName))
		Eventually(func() []string { return director.Calls("DeleteDeployment") }, 10*time.Second).Should(ConsistOf(deploymentName))
	})

	// expectNothingRecorded checks that the delete left no index entry, task
	// record, or metadata behind.
	expectNothingRecorded := func() {
		vaultClient := internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)

		_, indexed, err := vaultClient.FindInstance(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(indexed).To(BeFalse(), "the refused delete left an index entry")

		for _, path := range []string{"/task", "/metadata"} {
			var record map[string]interface{}

			exists, err := vaultClient.Get(ctx, instanceID+path, &record)
			Expect(err).ToNot(HaveOccurred())
			Expect(exists).To(BeFalse(), "the refused delete wrote %s%s", instanceID, path)
		}
	}

	// A deployment with a task still running on it, such as the create of a
	// provision whose index entry is gone, must not be deleted under it.
	It("refuses the delete while a BOSH task runs on the deployment", func() {
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			return &bosh.Task{ID: 55, State: "processing", Description: "create deployment"}, nil
		}

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, request, true)
		Expect(errors.Is(err, osbapi.ErrConcurrencyError)).To(BeTrue(), "the delete answered %v", err)
		Expect(async).To(BeFalse())
		Expect(director.Calls("FindRunningTaskForDeployment")).To(ContainElement(deploymentName))
		Consistently(func() []string { return director.Calls("DeleteDeployment") }, 500*time.Millisecond).Should(BeEmpty())

		expectNothingRecorded()
	})

	It("fails the delete when it cannot check for a running BOSH task", func() {
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) {
			return nil, errDirectorUnreachable
		}

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, request, true)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, osbapi.ErrGoneError)).To(BeFalse(), "a deployment that exists was answered as gone: %v", err)
		Expect(errors.Is(err, osbapi.ErrConcurrencyError)).To(BeFalse(), "an unknown task state was answered as a running task: %v", err)
		Expect(async).To(BeFalse())
		Consistently(func() []string { return director.Calls("DeleteDeployment") }, 500*time.Millisecond).Should(BeEmpty())

		expectNothingRecorded()
	})

	It("fails a delete it cannot record while the director still has the deployment", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		brokerInstance = newBroker(internalVault.New(proxy.Addr, suite.vault.RootToken, true))
		proxy.FailWrites("secret/data/db", http.StatusForbidden, "permission denied", -1)

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, request, true)
		Expect(err).To(HaveOccurred())
		Expect(errors.Is(err, osbapi.ErrGoneError)).To(BeFalse(), "a deployment that exists was answered as gone: %v", err)
		Expect(errors.Is(err, osbapi.ErrInstanceNotFound)).To(BeFalse(), "a deployment that exists was answered as not found: %v", err)
		Expect(async).To(BeFalse())
		Consistently(func() []string { return director.Calls("DeleteDeployment") }, 500*time.Millisecond).Should(BeEmpty())
	})

	It("answers 410 when the deployment is gone by the time the unrecorded delete checks again", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		var lookups sync.Map

		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if _, seen := lookups.LoadOrStore(name, true); !seen {
				return &bosh.DeploymentDetail{Name: name}, nil
			}

			return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
		}

		brokerInstance = newBroker(internalVault.New(proxy.Addr, suite.vault.RootToken, true))
		proxy.FailWrites("secret/data/db", http.StatusForbidden, "permission denied", -1)

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, request, true)
		Expect(errors.Is(err, osbapi.ErrGoneError)).To(BeTrue(), "the delete answered %v", err)
		Expect(async).To(BeFalse())
		Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
	})
})

// A delete whose index lookup fails must not answer 410. The broker cannot
// tell whether the instance is indexed, and the deployment named from the
// request's plan is not the instance's when the entry records another name,
// so the director's answer for that name says nothing about the instance.
var _ = Describe("A delete whose index lookup fails", func() {
	var (
		ctx            context.Context
		stopService    context.CancelFunc
		director       *testutil.ScriptedBOSHDirector
		vaultClient    *internalVault.Vault
		instanceID     string
		deploymentName string
		derivedName    string
	)

	BeforeEach(func() {
		ctx = context.Background()
		stopService = nil
		instanceID = fmt.Sprintf("lookup-fails-%d", time.Now().UnixNano())
		deploymentName = "renamed-" + instanceID
		derivedName = "standalone-" + instanceID

		director = testutil.NewScriptedBOSHDirector()
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.DeleteDeploymentFn = func(string) (*bosh.Task, error) { return &bosh.Task{ID: 91, State: "queued"}, nil }
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) { return &bosh.Task{ID: taskID, State: "processing"}, nil }
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if name == derivedName {
				return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
			}

			return &bosh.DeploymentDetail{Name: name}, nil
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		Expect(vaultClient.Index(ctx, instanceID, map[string]interface{}{
			"service_id":      "valkey",
			"plan_id":         "standalone",
			"deployment_name": deploymentName,
			"created_at":      time.Now().UTC().Format(time.RFC3339),
		})).To(Succeed())
	})

	AfterEach(func() {
		stopBrokerGoroutines(stopService)

		_ = vaultClient.Index(ctx, instanceID, nil)
	})

	deleteThrough := func(vaultAddr string) (int, string) {
		var serviceCtx context.Context

		serviceCtx, stopService = context.WithCancel(context.Background())

		brokerInstance := &broker.Broker{
			Plans:          map[string]services.Plan{},
			BOSH:           director,
			Vault:          internalVault.New(vaultAddr, suite.vault.RootToken, true),
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}

		server := osbHandlerServer(brokerInstance)
		defer server.Close()

		return sendOSBRequest(server.URL, http.MethodDelete, "/v2/service_instances/"+instanceID+"?service_id=valkey&plan_id=standalone&accepts_incomplete=true", nil)
	}

	It("a delete whose first index lookup fails answers 500", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		proxy.FailReads("secret/data/db", http.StatusForbidden, "permission denied", -1)

		status, body := deleteThrough(proxy.Addr)
		Expect(status).To(Equal(http.StatusInternalServerError), "body: %s", body)
		Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
	})

	// The lookup after the deprovision request is recorded fails, while the
	// lookups before it succeed.
	It("a delete whose lookup after recording the request fails answers 500", func() {
		target, err := url.Parse(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		reverse := httputil.NewSingleHostReverseProxy(target)

		var recorded atomic.Bool

		proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if strings.HasSuffix(request.URL.Path, "/secret/data/db") {
				switch request.Method {
				case http.MethodPut, http.MethodPost:
					recorded.Store(true)
				case http.MethodGet:
					if recorded.Load() {
						writer.Header().Set("Content-Type", "application/json")
						writer.WriteHeader(http.StatusForbidden)
						_, _ = writer.Write([]byte(`{"errors":["permission denied"]}`))

						return
					}
				}
			}

			reverse.ServeHTTP(writer, request)
		}))
		defer proxy.Close()

		status, body := deleteThrough(proxy.URL)

		Expect(recorded.Load()).To(BeTrue(), "the delete never recorded its request")
		Expect(status).To(Equal(http.StatusInternalServerError), "body: %s", body)
		Expect(director.Calls("DeleteDeployment")).To(BeEmpty())
	})
})

// The broker's orphan check removes index entries that have no BOSH
// deployment, and an accepted provision has no deployment until its deploy
// task starts. So the check spares an entry this process holds a claim for,
// one whose newest time is less than two hours old, and one whose task
// record shows a provision in progress. It removes an entry only when the
// director confirms that its deployment is gone and that no task runs on it,
// and only while the entry still equals the one it checked.
var _ = Describe("The broker's orphan check", func() {
	const planID = "standalone"

	var (
		ctx            context.Context
		director       *testutil.ScriptedBOSHDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		suffix         string
		seeded         []string
		live           map[string]bool
	)

	BeforeEach(func() {
		ctx = context.Background()
		suffix = strconv.FormatInt(time.Now().UnixNano(), 10)
		seeded = nil

		// The director lists the deployments a spec puts in live. An empty
		// list stops the check, so one unrelated deployment is always there.
		live = map[string]bool{"unrelated-" + suffix: true}

		director = testutil.NewScriptedBOSHDirector()
		director.GetDeploymentsFn = func() ([]bosh.Deployment, error) {
			deployments := []bosh.Deployment{}

			for name := range live {
				deployments = append(deployments, bosh.Deployment{Name: name})
			}

			return deployments, nil
		}

		// Asked for one deployment, the director finds the ones in live and
		// answers its deployment-not-found code for any other, and it runs no
		// tasks.
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if live[name] {
				return &bosh.DeploymentDetail{Name: name}, nil
			}

			return deploymentNotFound(name)
		}
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil }

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance = &broker.Broker{BOSH: director, Vault: vaultClient, Config: &config.Config{}}
	})

	AfterEach(func() {
		for _, id := range seeded {
			_ = vaultClient.Index(ctx, id, nil)
		}
	})

	// seedEntry indexes an instance with no deployment, requested at the
	// given time, and returns its ID and the deployment name it maps to. A
	// field that extra sets to nil is left out of the entry.
	seedEntry := func(name string, requestedAt time.Time, extra map[string]interface{}) (string, string) {
		id := name + "-" + suffix
		entry := map[string]interface{}{
			"service_id":   "valkey",
			"plan_id":      planID,
			"requested_at": requestedAt.Format(time.RFC3339),
		}

		for key, value := range extra {
			if value == nil {
				delete(entry, key)

				continue
			}

			entry[key] = value
		}

		Expect(vaultClient.Index(ctx, id, entry)).To(Succeed())

		seeded = append(seeded, id)

		return id, planID + "-" + id
	}

	indexed := func(id string) bool {
		_, exists, err := vaultClient.FindInstance(ctx, id)
		Expect(err).ToNot(HaveOccurred())

		return exists
	}

	old := time.Now().Add(-3 * time.Hour)

	It("the orphan check keeps a young entry and a claimed entry", func() {
		youngID, youngName := seedEntry("young", time.Now(), nil)
		createdID, createdName := seedEntry("created", old, map[string]interface{}{"created": time.Now().Unix()})
		claimedID, claimedName := seedEntry("claimed", old, nil)

		release := brokerInstance.HoldOperationClaim(claimedID, "provision")
		defer release()

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElements(youngName))
		Expect(removed).ToNot(ContainElements(createdName))
		Expect(removed).ToNot(ContainElements(claimedName))
		Expect(indexed(youngID)).To(BeTrue(), "a young entry was removed")
		Expect(indexed(createdID)).To(BeTrue(), "an entry created moments ago was removed")
		Expect(indexed(claimedID)).To(BeTrue(), "an entry this process holds a claim for was removed")
	})

	// The reconciler dates an entry it adopted by discovered_at, and a delete
	// of an instance the index did not have leaves only
	// deprovision_requested_at. Either time shows a young entry.
	It("keeps a young entry dated only by a time the broker does not write at provision", func() {
		now := time.Now().UTC().Format(time.RFC3339)

		discoveredID, discoveredName := seedEntry("discovered", old, map[string]interface{}{"requested_at": nil, "discovered_at": now})
		deleteID, deleteName := seedEntry("delete-requested", old, map[string]interface{}{"requested_at": nil, "deprovision_requested_at": now})

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(discoveredName))
		Expect(removed).ToNot(ContainElement(deleteName))
		Expect(indexed(discoveredID)).To(BeTrue(), "an entry discovered moments ago was removed")
		Expect(indexed(deleteID)).To(BeTrue(), "an entry whose delete was requested moments ago was removed")
	})

	It("the orphan check still removes an old entry with no deployment", func() {
		oldID, oldName := seedEntry("old", old, map[string]interface{}{"created": old.Unix()})

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(ContainElement(oldName))
		Expect(indexed(oldID)).To(BeFalse())
	})

	// The reconciler can rewrite an entry's plan ID after the deployment was
	// created, so the name built from the plan ID no longer matches the
	// deployment. The name the entry records still does.
	It("the orphan check keeps an entry whose stored deployment name exists", func() {
		adoptedID, builtName := seedEntry("adopted", old, map[string]interface{}{"created": old.Unix()})
		storedName := "standalone-classic-" + adoptedID
		Expect(vaultClient.UpdateIndexEntry(ctx, adoptedID, map[string]interface{}{"deployment_name": storedName})).To(Succeed())

		live[storedName] = true

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(storedName))
		Expect(removed).ToNot(ContainElement(builtName))
		Expect(indexed(adoptedID)).To(BeTrue(), "the entry of a live deployment was removed")
	})

	// An empty list is more likely a failed scan than an empty director, so
	// the check removes nothing, even an entry whose deployment is gone.
	It("the orphan check removes nothing when the deployment list is empty", func() {
		goneID, goneName := seedEntry("empty-list", old, map[string]interface{}{"created": old.Unix()})

		live = map[string]bool{}

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(BeEmpty())
		Expect(removed).ToNot(ContainElement(goneName))
		Expect(indexed(goneID)).To(BeTrue(), "an entry was removed on an empty deployment list")
	})

	// The deployment list alone never proves a deployment gone. The director
	// is asked for the deployment itself, and only its deployment-not-found
	// code counts, and a task still running on the deployment keeps the
	// entry as well. Any answer that leaves this unknown keeps the entry.
	confirmations := []struct {
		name  string
		setup func()
	}{
		{"the orphan check keeps an entry whose deployment the director still has", func() {
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				return &bosh.DeploymentDetail{Name: name}, nil
			}
		}},
		{"the orphan check keeps an entry whose deployment the director cannot confirm is gone", func() {
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnreachable }
		}},
		{"the orphan check keeps an entry whose deployment has a running task", func() {
			director.FindRunningTaskForDeploymentFn = func(name string) (*bosh.Task, error) {
				return &bosh.Task{ID: 88, State: "processing", Description: "create deployment", Deployment: name}, nil
			}
		}},
		{"the orphan check keeps an entry when it cannot check for a running task", func() {
			director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, errDirectorUnreachable }
		}},
	}

	for _, confirmation := range confirmations {
		It(confirmation.name, func() {
			keptID, keptName := seedEntry("unconfirmed", old, map[string]interface{}{"created": old.Unix()})

			confirmation.setup()

			removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).ToNot(ContainElement(keptName))
			Expect(indexed(keptID)).To(BeTrue(), "an entry was removed before the director confirmed its deployment gone")
			Expect(director.Calls("GetDeployment")).To(ContainElement(keptName), "the director was not asked for the deployment")
		})
	}

	It("the orphan check removes an entry once the director confirms its deployment is gone", func() {
		goneID, goneName := seedEntry("confirmed-gone", old, map[string]interface{}{"created": old.Unix()})

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(ContainElement(goneName))
		Expect(indexed(goneID)).To(BeFalse())
		Expect(director.Calls("GetDeployment")).To(ContainElement(goneName))
		Expect(director.Calls("FindRunningTaskForDeployment")).To(ContainElement(goneName))
	})

	// An entry indexed before deployment_name existed is named from its plan
	// ID, so its deployment is still found, and it is still removed once the
	// deployment is gone.
	It("an entry without a stored name still uses the plan and instance ID", func() {
		liveID, liveName := seedEntry("unnamed-live", old, map[string]interface{}{"created": old.Unix()})
		goneID, goneName := seedEntry("unnamed-gone", old, map[string]interface{}{"created": old.Unix()})

		live[liveName] = true

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(liveName))
		Expect(indexed(liveID)).To(BeTrue(), "the entry of a live deployment was removed")
		Expect(removed).To(ContainElement(goneName))
		Expect(indexed(goneID)).To(BeFalse())
	})

	It("keeps an old entry whose provision task record is in progress", func() {
		runningID, runningName := seedEntry("running", old, nil)
		brokerInstance.RecordTask(ctx, runningID, "provision", "BOSH deployment in progress (task 77)", 77)

		failedID, failedName := seedEntry("failed", old, nil)
		brokerInstance.RecordTask(ctx, failedID, "provision", "BOSH deployment task 77 is error", -1)

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(runningName))
		Expect(indexed(runningID)).To(BeTrue(), "an entry whose provision is in progress was removed")
		Expect(removed).To(ContainElement(failedName), "an entry whose provision failed was kept")
		Expect(indexed(failedID)).To(BeFalse())
	})

	// writeTask stores a provision task record in progress, with updated_at
	// only when updatedAt is not nil.
	writeTask := func(taskInstanceID string, updatedAt *time.Time) {
		task := map[string]interface{}{
			"action":      "provision",
			"task":        77,
			"state":       "in_progress",
			"description": "BOSH deployment in progress (task 77)",
		}

		if updatedAt != nil {
			task["updated_at"] = updatedAt.Unix()
		}

		Expect(vaultClient.Put(ctx, taskInstanceID+"/task", task)).To(Succeed())
	}

	It("removes an old entry whose provision task record has not been updated for two hours", func() {
		staleID, staleName := seedEntry("stale", old, nil)
		writeTask(staleID, &old)

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(ContainElement(staleName), "an entry whose provision stopped updating three hours ago was kept")
		Expect(indexed(staleID)).To(BeFalse())
	})

	// A provision running in another broker process holds no claim here, and
	// a record with no update time cannot show how old it is.
	It("keeps an old entry whose provision task record has no update time", func() {
		undatedID, undatedName := seedEntry("undated-task", old, nil)
		writeTask(undatedID, nil)

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(undatedName))
		Expect(indexed(undatedID)).To(BeTrue(), "an entry whose provision record has no update time was removed")
	})

	It("keeps an entry whose task record cannot be read", func() {
		proxy, err := testutil.NewVaultFaultProxy(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		defer proxy.Close()

		unreadableID, unreadableName := seedEntry("unreadable", old, nil)

		brokerInstance.Vault = internalVault.New(proxy.Addr, suite.vault.RootToken, true)
		proxy.FailReads(unreadableID+"/task", http.StatusForbidden, "permission denied", -1)

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(unreadableName))
		Expect(indexed(unreadableID)).To(BeTrue())
	})

	// duringCheck runs act once, when the orphan check reads the task record
	// of instanceID, which falls between the check of the entry and its
	// removal.
	duringCheck := func(instanceID string, act func()) *httptest.Server {
		target, err := url.Parse(suite.vault.Addr)
		Expect(err).ToNot(HaveOccurred())

		reverse := httputil.NewSingleHostReverseProxy(target)

		var once sync.Once

		return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/"+instanceID+"/task") {
				once.Do(act)
			}

			reverse.ServeHTTP(writer, request)
		}))
	}

	It("keeps an entry that changed during the check", func() {
		changedID, changedName := seedEntry("changed", old, nil)

		proxy := duringCheck(changedID, func() {
			Expect(vaultClient.UpdateIndexEntry(ctx, changedID, map[string]interface{}{"status": "provisioning_started"})).To(Succeed())
		})
		defer proxy.Close()

		brokerInstance.Vault = internalVault.New(proxy.URL, suite.vault.RootToken, true)

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).ToNot(ContainElement(changedName), "the check reported an entry it did not remove")
		Expect(indexed(changedID)).To(BeTrue(), "an entry that changed after it was checked was removed")
	})

	It("keeps an entry whose claim was taken during the check", func() {
		claimedID, claimedName := seedEntry("late-claim", old, nil)

		var release func()

		proxy := duringCheck(claimedID, func() { release = brokerInstance.HoldOperationClaim(claimedID, "provision") })
		defer proxy.Close()

		brokerInstance.Vault = internalVault.New(proxy.URL, suite.vault.RootToken, true)

		removed, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(release).ToNot(BeNil(), "the check never read the entry's task record")

		release()

		Expect(removed).ToNot(ContainElement(claimedName))
		Expect(indexed(claimedID)).To(BeTrue(), "an entry claimed during the check was removed")
	})
})

// The provision completion hook reads an instance's details from
// <instance>/<deployment> and hands the deployment name to the vm-monitor and
// the VM lookup. The name comes from the index entry, because the reconciler
// can rewrite the entry's plan ID after the deployment was created.
var _ = Describe("An instance's provision details", func() {
	const planID = "standalone"

	var (
		ctx            context.Context
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		request        osbapi.ProvisionRequest
	)

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("details-%d", time.Now().UnixNano())
		request = osbapi.ProvisionRequest{
			ServiceID:        "valkey",
			PlanID:           planID,
			OrganizationGUID: "org-guid",
			SpaceGUID:        "space-guid",
			Parameters:       map[string]any{"memory": "small"},
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance = &broker.Broker{BOSH: testutil.NewScriptedBOSHDirector(), Vault: vaultClient, Config: &config.Config{}}

		Expect(brokerInstance.RecordInitialRequest(ctx, instanceID, request)).To(Succeed())
	})

	AfterEach(func() {
		_ = vaultClient.Index(ctx, instanceID, nil)
	})

	It("instance details name the stored deployment", func() {
		storedName := "standalone-classic-" + instanceID

		Expect(brokerInstance.StoreDeploymentDetails(ctx, instanceID, storedName, request)).To(Succeed())
		brokerInstance.StoreDeploymentNameInIndex(ctx, instanceID, storedName)

		details, deployment, err := brokerInstance.InstanceDetails(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(deployment).To(Equal(storedName))
		Expect(details.PlanID).To(Equal(planID))
		Expect(details.Parameters).To(HaveKeyWithValue("memory", "small"))
	})

	It("instance details of an entry without a stored name use the plan and instance ID", func() {
		builtName := planID + "-" + instanceID

		Expect(brokerInstance.StoreDeploymentDetails(ctx, instanceID, builtName, request)).To(Succeed())

		details, deployment, err := brokerInstance.InstanceDetails(ctx, instanceID)
		Expect(err).ToNot(HaveOccurred())
		Expect(deployment).To(Equal(builtName))
		Expect(details.PlanID).To(Equal(planID))
		Expect(details.Parameters).To(HaveKeyWithValue("memory", "small"))
	})
})

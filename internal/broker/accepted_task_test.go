package broker_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	vaultPkg "blacksmith/pkg/vault"
	"blacksmith/shield"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// A recorded operation names a task only when its task ID is above zero.
// Zero means no task yet. Every other ID is one the director named.
func TestHasTaskIsTrueOnlyForATaskIDAboveZero(t *testing.T) {
	t.Parallel()

	cases := []struct {
		taskID int
		want   bool
	}{
		{taskID: 0, want: false},
		{taskID: 1, want: true},
		{taskID: 2, want: true},
	}

	for _, testCase := range cases {
		recorded := broker.RecordedOperation{TaskID: testCase.taskID}
		if got := recorded.HasTask(); got != testCase.want {
			t.Errorf("HasTask with task ID %d = %t, want %t", testCase.taskID, got, testCase.want)
		}
	}
}

// The director names the task of a deploy or a delete in its answer, and
// the broker records that task as soon as the director accepts. It reads
// no deployment events to name it.
var _ = Describe("Director tasks recorded on acceptance", func() {
	const (
		acceptedCreateTaskID = 77
		acceptedDeleteTaskID = 300
	)

	var (
		ctx            context.Context
		director       *contextDirector
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		deploymentName string
		shutdown       context.CancelFunc
		dir            string
		eventReads     atomic.Int32
	)

	readRecord := func() map[string]interface{} {
		record, err := suite.vault.ReadSecret(instanceID + "/task")
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return record
	}

	recorded := func() broker.RecordedOperation {
		operation, err := brokerInstance.RecordedOperation(ctx, instanceID)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())

		return operation
	}

	settled := func() bool {
		return !brokerInstance.ProvisionActive(instanceID) && !brokerInstance.DeprovisionActive(instanceID)
	}

	BeforeEach(func() {
		ctx = context.Background()
		instanceID = fmt.Sprintf("accepted-%d", time.Now().UnixNano())
		deploymentName = standalonePlanID + "-" + instanceID

		eventReads.Store(0)

		var serviceCtx context.Context

		serviceCtx, shutdown = context.WithCancel(ctx)

		director = &contextDirector{ScriptedBOSHDirector: testutil.NewScriptedBOSHDirector()}
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: outcomeDirectorUUID}, nil }
		director.GetReleasesFn = func() ([]bosh.Release, error) { return []bosh.Release{}, nil }
		director.FindRunningTaskForDeploymentFn = func(string) (*bosh.Task, error) { return nil, nil } //nolint:nilnil // no running task is a nil task without an error
		director.onGetEvents = func(context.Context, string) ([]bosh.Event, error) {
			eventReads.Add(1)

			return nil, nil
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance, dir = newOutcomeBroker(director, vaultClient, serviceCtx)
	})

	AfterEach(func() {
		stopBrokerGoroutines(shutdown)

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	provision := func() {
		_, async, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: valkeyServiceID, PlanID: standalonePlanID, OrganizationGUID: outcomeOrgGUID, SpaceGUID: outcomeSpaceGUID,
		}, true)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		ExpectWithOffset(1, async).To(BeTrue())
	}

	It("provision records the task before it finishes", func() {
		director.onCreateDeployment = func(context.Context, string) (*bosh.Task, error) {
			return &bosh.Task{ID: acceptedCreateTaskID, State: outcomeTaskQueued}, nil
		}
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
		}

		provision()

		Eventually(settled, 10*time.Second).Should(BeTrue())

		operation := recorded()
		Expect(operation.TaskID).To(Equal(acceptedCreateTaskID))
		Expect(operation.HasTask()).To(BeTrue())
		Expect(readRecord()["description"]).To(ContainSubstring(fmt.Sprintf("task %d", acceptedCreateTaskID)))
		Expect(eventReads.Load()).To(BeZero(), "the broker read the deployment's events to name a task the director already named")
	})

	It("provision fails with the director's description when the deploy is rejected", func() {
		const rejection = "Manifest has no stemcells"

		director.onCreateDeployment = func(context.Context, string) (*bosh.Task, error) {
			return nil, fmt.Errorf("deploy: %w", &boshapi.APIError{
				Method: http.MethodPost, Path: "/deployments", StatusCode: http.StatusBadRequest, Code: 440001, Description: rejection,
			})
		}

		provision()

		Eventually(settled, 10*time.Second).Should(BeTrue())

		record := readRecord()
		Expect(record).To(HaveKeyWithValue("state", outcomeStateFailed))
		Expect(record["description"]).To(ContainSubstring(rejection))
		Expect(record["description"]).To(ContainSubstring("HTTP 400"))
		Expect(record["description"]).To(ContainSubstring(deploymentName))
		Expect(recorded().HasTask()).To(BeFalse())
		Expect(eventReads.Load()).To(BeZero(), "the broker read the deployment's events after a deploy the director refused")
	})

	It("deprovision follows the delete task the redirect names", func() {
		Expect(vaultClient.Index(ctx, instanceID, vaultPkg.Instance{
			ID: instanceID, ServiceID: valkeyServiceID, PlanID: standalonePlanID, DeploymentName: deploymentName,
		})).To(Succeed())

		director.onGetDeployment = func(context.Context, string) (*bosh.DeploymentDetail, error) {
			return &bosh.DeploymentDetail{Name: deploymentName, Manifest: "name: " + deploymentName}, nil
		}
		director.onDeleteDeployment = func(context.Context, string) (*bosh.Task, error) {
			return &bosh.Task{ID: acceptedDeleteTaskID, State: outcomeTaskQueued}, nil
		}
		director.GetTaskFn = func(taskID int) (*bosh.Task, error) {
			return &bosh.Task{ID: taskID, State: outcomeTaskProcessing}, nil
		}

		_, async, err := brokerInstance.Deprovision(ctx, instanceID, osbapi.DeprovisionRequest{ServiceID: valkeyServiceID, PlanID: standalonePlanID}, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())

		Eventually(func() int { return recorded().TaskID }, 10*time.Second).Should(Equal(acceptedDeleteTaskID))
		Expect(recorded().Action).To(Equal(outcomeDeprovision))
		Expect(eventReads.Load()).To(BeZero(), "the broker read the deployment's events to name a delete task the director already named")
	})
})

// A provision sends its deploy with the context ID it recorded, and when
// the deploy's answer is lost, the unknown outcome keeps that ID. A plain
// test, because the director is a TLS test server driven through the real
// director adapter.
//
//nolint:paralleltest // t.Setenv cannot be used with t.Parallel
func TestProvisionSendsTheContextIDItRecordedAndKeepsItWhenTheAnswerIsLost(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	vault, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to start Vault: %v", err)
	}

	defer vault.Close()

	instanceID := fmt.Sprintf("recorded-%d", time.Now().UnixNano())

	var (
		sentMu   sync.Mutex
		sentID   string
		recordID interface{}
		deploys  int
	)

	director := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/deployments":
			record, _ := vault.ReadSecret(instanceID + "/task")

			sentMu.Lock()
			deploys++
			sentID = request.Header.Get("X-Bosh-Context-Id")
			recordID = record["context_id"]
			sentMu.Unlock()

			boshtest.DropAnswer(writer)

			return true
		case request.Method == http.MethodGet && (request.URL.Path == "/releases" || request.URL.Path == "/stemcells" || request.URL.Path == "/tasks"):
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})

			return true
		default:
			return false
		}
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

	dir := t.TempDir()
	initScript := filepath.Join(dir, "init")

	err = os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)
	if err != nil {
		t.Fatalf("failed to write the init script: %v", err)
	}

	plan := services.Plan{
		ID:             standalonePlanID,
		Name:           standalonePlanID,
		Type:           valkeyServiceID,
		InitScriptPath: initScript,
		Manifest: map[interface{}]interface{}{
			manifestNameKey:           outcomeManifestName,
			manifestInstanceGroupsKey: []interface{}{},
		},
		Service: &services.Service{ID: valkeyServiceID, Name: valkeyServiceID},
	}

	serviceCtx, stopService := context.WithCancel(context.Background())
	defer waitForBrokerGoroutines(t, stopService)

	brokerInstance := &broker.Broker{
		Plans:          map[string]services.Plan{valkeyServiceID + "/" + standalonePlanID: plan},
		BOSH:           adapter,
		Vault:          internalVault.New(vault.Addr, vault.RootToken, true),
		Shield:         &shield.NoopClient{},
		Config:         &config.Config{},
		InstanceLocks:  make(map[string]*sync.Mutex),
		ServiceContext: serviceCtx,
	}

	ctx := context.Background()

	_, _, err = brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
		ServiceID: valkeyServiceID, PlanID: standalonePlanID, OrganizationGUID: outcomeOrgGUID, SpaceGUID: outcomeSpaceGUID,
	}, true)
	if err != nil {
		t.Fatalf("the provision was refused: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)

	var operation broker.RecordedOperation

	for {
		operation, err = brokerInstance.RecordedOperation(ctx, instanceID)
		if err == nil && operation.State == outcomeStateUnknown {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("the provision never recorded an unknown outcome; last record %+v, error %v", operation, err)
		}

		time.Sleep(20 * time.Millisecond)
	}

	sentMu.Lock()
	defer sentMu.Unlock()

	if deploys != 1 {
		t.Fatalf("the director got %d deploys, want 1", deploys)
	}

	if sentID == "" {
		t.Fatal("the deploy carried no context ID")
	}

	if recordID != sentID {
		t.Errorf("the deploy carried context ID %q, but the record held %v while it was sent", sentID, recordID)
	}

	if operation.ContextID != sentID {
		t.Errorf("the unknown outcome keeps context ID %q, want the %q the deploy carried", operation.ContextID, sentID)
	}
}

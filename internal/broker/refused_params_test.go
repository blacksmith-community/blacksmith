package broker_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	"blacksmith/shield"

	graftlog "github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// refusedProbe is the request text the refused provisions carry. Neither
// the platform's answer, the recorded operation, nor the log may echo it.
const refusedProbe = "probe-refused-7f3a"

// A provision whose parameters the plan puts where graft reads code, a
// path, or a name fails as the tenant's mistake, in words that quote
// nothing from the request or the plan.
var _ = Describe("Provision parameters graft refuses", func() {
	var (
		ctx            context.Context
		stopService    context.CancelFunc
		capture        *captureLogger
		previousLogger logger.Logger
		vaultClient    *internalVault.Vault
		brokerInstance *broker.Broker
		instanceID     string
		createCalled   chan struct{}
		dir            string
		previousStderr func(format string, args ...interface{})
		graftMu        sync.Mutex
		graftLines     []string
	)

	const (
		serviceID = "refused-service"
		planID    = "refused-plan"
	)

	// provisionWith provisions with the plan's vm_type set to template and
	// the probe in the request, waits for the provision to end, and returns
	// the answer to the request and the last operation.
	provisionWith := func(template string) (osbapi.ProvisionResponse, osbapi.LastOperationResponse) {
		initScript := filepath.Join(dir, "init")
		Expect(os.WriteFile(initScript, []byte("#!/bin/bash\nexit 0\n"), 0o600)).To(Succeed())

		brokerInstance.Plans = map[string]services.Plan{serviceID + "/" + planID: {
			ID:             planID,
			Name:           planID,
			Type:           "standalone",
			InitScriptPath: initScript,
			Manifest: map[string]interface{}{
				"name":            "placeholder",
				"meta":            map[string]interface{}{"sizes": map[string]interface{}{"small": "vm-small"}},
				"instance_groups": []interface{}{map[string]interface{}{"name": "standalone", "vm_type": template}},
			},
			Service: &services.Service{ID: serviceID, Name: serviceID},
		}}

		accepted, async, err := brokerInstance.Provision(ctx, instanceID, osbapi.ProvisionRequest{
			ServiceID: serviceID, PlanID: planID, OrganizationGUID: "org-guid", SpaceGUID: "space-guid",
			Parameters: map[string]any{"size": refusedProbe, "path": "secret/" + refusedProbe + ":key"},
		}, true)
		Expect(err).ToNot(HaveOccurred())
		Expect(async).To(BeTrue())

		Eventually(func() bool { return brokerInstance.ProvisionActive(instanceID) }, 10*time.Second).Should(BeFalse())

		response, err := brokerInstance.LastOperation(ctx, instanceID, osbapi.LastOperationRequest{
			ServiceID: serviceID, PlanID: planID, Operation: "provision",
		})
		Expect(err).ToNot(HaveOccurred())

		return accepted, response
	}

	// graftOutput returns what graft wrote to its stderr hook.
	graftOutput := func() string {
		graftMu.Lock()
		defer graftMu.Unlock()

		return strings.Join(graftLines, "\n")
	}

	BeforeEach(func() {
		var serviceCtx context.Context

		ctx = context.Background()
		serviceCtx, stopService = context.WithCancel(ctx)
		instanceID = fmt.Sprintf("refused-%d", time.Now().UnixNano())
		createCalled = make(chan struct{}, 1)

		var err error

		dir, err = os.MkdirTemp("", "blacksmith-refused")
		Expect(err).ToNot(HaveOccurred())

		capture = newCaptureLogger()
		previousLogger = logger.Get()

		logger.Set(capture)

		graftLines = nil
		previousStderr = graftlog.PrintStdErrf
		graftlog.PrintStdErrf = func(format string, args ...interface{}) {
			graftMu.Lock()
			defer graftMu.Unlock()

			graftLines = append(graftLines, fmt.Sprintf(format, args...))
		}

		director := testutil.NewScriptedBOSHDirector()
		director.GetInfoFn = func() (*bosh.Info, error) { return &bosh.Info{UUID: "director-uuid"}, nil }
		director.CreateDeploymentFn = func(string) (*bosh.Task, error) {
			createCalled <- struct{}{}

			return &bosh.Task{ID: 1, State: "processing"}, nil
		}

		vaultClient = internalVault.New(suite.vault.Addr, suite.vault.RootToken, true)
		brokerInstance = &broker.Broker{
			BOSH:           director,
			Vault:          vaultClient,
			Shield:         &shield.NoopClient{},
			Config:         &config.Config{},
			InstanceLocks:  make(map[string]*sync.Mutex),
			ServiceContext: serviceCtx,
		}
	})

	AfterEach(func() {
		logger.Set(previousLogger)
		stopBrokerGoroutines(stopService)

		graftlog.PrintStdErrf = previousStderr

		_ = vaultClient.Index(ctx, instanceID, nil)
		_ = os.RemoveAll(dir)
	})

	for name, template := range map[string]string{
		"a bracket key": "(( grab meta.sizes[meta.params.size] ))",
		"a vault path":  "(( vault meta.params.path ))",
	} {
		It("fails the provision without echoing the request when a parameter is "+name, func() {
			accepted, response := provisionWith(template)

			Expect(response.State).To(Equal(osbapi.StateFailed))
			Expect(response.Description).To(Equal("Manifest generation failed: " +
				"the request parameters cannot be used where this plan puts them, " +
				"because the plan reads that position as code, a path, or a name"))
			Expect(createCalled).ToNot(Receive(), "the broker deployed a manifest it failed to render")

			recorded, err := brokerInstance.RecordedOperation(ctx, instanceID)
			Expect(err).ToNot(HaveOccurred())
			Expect(recorded.Description).To(Equal(response.Description))

			output := capture.output()
			Expect(output).To(ContainSubstring("Failed to generate the manifest of plan " + planID + " for instance " + instanceID +
				", because a request parameter is where the plan reads code, a path, or a name"))

			answer, err := json.Marshal(accepted)
			Expect(err).ToNot(HaveOccurred())

			for _, banned := range []string{refusedProbe, "((", "$."} {
				Expect(string(answer)).ToNot(ContainSubstring(banned))
				Expect(response.Description).ToNot(ContainSubstring(banned))
				Expect(output).ToNot(ContainSubstring(banned))
				Expect(graftOutput()).ToNot(ContainSubstring(banned))
			}
		})
	}
})

package testutil

import (
	"context"
	"sync"

	"blacksmith/internal/bosh"
)

// ScriptedBOSHDirector is a bosh.Director whose deployment and task methods are
// driven by per-test functions. Every method without a function assigned falls
// through to IntegrationMockBOSH, which reports "not implemented". Calls to the
// scripted methods are recorded so tests can assert on what the broker asked
// the director to do.
type ScriptedBOSHDirector struct {
	*IntegrationMockBOSH

	GetInfoFn                      func() (*bosh.Info, error)
	GetDeploymentFn                func(name string) (*bosh.DeploymentDetail, error)
	GetDeploymentsFn               func() ([]bosh.Deployment, error)
	GetDeploymentVMsFn             func(deployment string) ([]bosh.VM, error)
	CreateDeploymentFn             func(manifest string) (*bosh.Task, error)
	DeleteDeploymentFn             func(name string) (*bosh.Task, error)
	GetTaskFn                      func(taskID int) (*bosh.Task, error)
	WaitForTaskFn                  func(taskID int) (*bosh.Task, error)
	GetEventsFn                    func(deployment string) ([]bosh.Event, error)
	GetReleasesFn                  func() ([]bosh.Release, error)
	FindRunningTaskForDeploymentFn func(deployment string) (*bosh.Task, error)
	FindTaskByContextIDFn          func(contextID string) (*bosh.Task, error)
	FetchLogsFn                    func(ctx context.Context, deployment, job, instanceID string, budget *bosh.LogBudget) (*bosh.LogBundle, error)

	mu    sync.Mutex
	calls map[string][]string
}

// NewScriptedBOSHDirector creates a ScriptedBOSHDirector with no scripted methods.
func NewScriptedBOSHDirector() *ScriptedBOSHDirector {
	return &ScriptedBOSHDirector{
		IntegrationMockBOSH: NewIntegrationMockBOSH(),
		calls:               make(map[string][]string),
	}
}

// Calls returns the recorded arguments for every invocation of the named method.
func (s *ScriptedBOSHDirector) Calls(method string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	recorded := make([]string, len(s.calls[method]))
	copy(recorded, s.calls[method])

	return recorded
}

// GetInfo runs GetInfoFn when set.
func (s *ScriptedBOSHDirector) GetInfo(ctx context.Context) (*bosh.Info, error) {
	s.record("GetInfo", "")

	if s.GetInfoFn != nil {
		return s.GetInfoFn()
	}

	return s.IntegrationMockBOSH.GetInfo(ctx)
}

// GetDeployment runs GetDeploymentFn when set.
func (s *ScriptedBOSHDirector) GetDeployment(ctx context.Context, name string) (*bosh.DeploymentDetail, error) {
	s.record("GetDeployment", name)

	if s.GetDeploymentFn != nil {
		return s.GetDeploymentFn(name)
	}

	return s.IntegrationMockBOSH.GetDeployment(ctx, name)
}

// GetDeployments runs GetDeploymentsFn when set.
func (s *ScriptedBOSHDirector) GetDeployments(ctx context.Context) ([]bosh.Deployment, error) {
	s.record("GetDeployments", "")

	if s.GetDeploymentsFn != nil {
		return s.GetDeploymentsFn()
	}

	return s.IntegrationMockBOSH.GetDeployments(ctx)
}

// GetDeploymentVMs runs GetDeploymentVMsFn when set.
func (s *ScriptedBOSHDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]bosh.VM, error) {
	s.record("GetDeploymentVMs", deployment)

	if s.GetDeploymentVMsFn != nil {
		return s.GetDeploymentVMsFn(deployment)
	}

	return s.IntegrationMockBOSH.GetDeploymentVMs(ctx, deployment)
}

// CreateDeployment runs CreateDeploymentFn when set.
func (s *ScriptedBOSHDirector) CreateDeployment(ctx context.Context, manifest string) (*bosh.Task, error) {
	s.record("CreateDeployment", bosh.ExtractDeploymentName(manifest))

	if s.CreateDeploymentFn != nil {
		return s.CreateDeploymentFn(manifest)
	}

	return s.IntegrationMockBOSH.CreateDeployment(ctx, manifest)
}

// DeleteDeployment runs DeleteDeploymentFn when set.
func (s *ScriptedBOSHDirector) DeleteDeployment(ctx context.Context, name string) (*bosh.Task, error) {
	s.record("DeleteDeployment", name)

	if s.DeleteDeploymentFn != nil {
		return s.DeleteDeploymentFn(name)
	}

	return s.IntegrationMockBOSH.DeleteDeployment(ctx, name)
}

// GetTask runs GetTaskFn when set.
func (s *ScriptedBOSHDirector) GetTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	s.record("GetTask", "")

	if s.GetTaskFn != nil {
		return s.GetTaskFn(taskID)
	}

	return s.IntegrationMockBOSH.GetTask(ctx, taskID)
}

// WaitForTask runs WaitForTaskFn when set.
func (s *ScriptedBOSHDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	s.record("WaitForTask", "")

	if s.WaitForTaskFn != nil {
		return s.WaitForTaskFn(taskID)
	}

	return s.IntegrationMockBOSH.WaitForTask(ctx, taskID)
}

// GetEvents runs GetEventsFn when set.
func (s *ScriptedBOSHDirector) GetEvents(ctx context.Context, deployment string) ([]bosh.Event, error) {
	s.record("GetEvents", deployment)

	if s.GetEventsFn != nil {
		return s.GetEventsFn(deployment)
	}

	return s.IntegrationMockBOSH.GetEvents(ctx, deployment)
}

// GetReleases runs GetReleasesFn when set.
func (s *ScriptedBOSHDirector) GetReleases(ctx context.Context) ([]bosh.Release, error) {
	s.record("GetReleases", "")

	if s.GetReleasesFn != nil {
		return s.GetReleasesFn()
	}

	return s.IntegrationMockBOSH.GetReleases(ctx)
}

// FindRunningTaskForDeployment runs FindRunningTaskForDeploymentFn when set.
func (s *ScriptedBOSHDirector) FindRunningTaskForDeployment(ctx context.Context, deployment string) (*bosh.Task, error) {
	s.record("FindRunningTaskForDeployment", deployment)

	if s.FindRunningTaskForDeploymentFn != nil {
		return s.FindRunningTaskForDeploymentFn(deployment)
	}

	return s.IntegrationMockBOSH.FindRunningTaskForDeployment(ctx, deployment)
}

// FindRunningTasksForDeployment answers with the task
// FindRunningTaskForDeploymentFn gives, as a list, when that is set.
func (s *ScriptedBOSHDirector) FindRunningTasksForDeployment(ctx context.Context, deployment string) ([]bosh.Task, error) {
	s.record("FindRunningTasksForDeployment", deployment)

	if s.FindRunningTaskForDeploymentFn == nil {
		return s.IntegrationMockBOSH.FindRunningTasksForDeployment(ctx, deployment)
	}

	task, err := s.FindRunningTaskForDeploymentFn(deployment)
	if err != nil || task == nil {
		return nil, err
	}

	return []bosh.Task{*task}, nil
}

// FindTaskByContextID runs FindTaskByContextIDFn when set.
func (s *ScriptedBOSHDirector) FindTaskByContextID(ctx context.Context, contextID string) (*bosh.Task, error) {
	s.record("FindTaskByContextID", contextID)

	if s.FindTaskByContextIDFn != nil {
		return s.FindTaskByContextIDFn(contextID)
	}

	return s.IntegrationMockBOSH.FindTaskByContextID(ctx, contextID)
}

// FetchLogs runs FetchLogsFn when set. It records the job and instance ID as
// "<job>/<instanceID>".
func (s *ScriptedBOSHDirector) FetchLogs(ctx context.Context, deployment, job, instanceID string, budget *bosh.LogBudget) (*bosh.LogBundle, error) {
	s.record("FetchLogs", job+"/"+instanceID)

	if s.FetchLogsFn != nil {
		return s.FetchLogsFn(ctx, deployment, job, instanceID, budget)
	}

	return s.IntegrationMockBOSH.FetchLogs(ctx, deployment, job, instanceID, budget)
}

func (s *ScriptedBOSHDirector) record(method, arg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls[method] = append(s.calls[method], arg)
}

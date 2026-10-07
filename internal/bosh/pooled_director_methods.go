package bosh

import (
	"context"
	"errors"
	"fmt"
)

// Static error variables to satisfy err113.
var (
	ErrUnexpectedResultType = errors.New("unexpected result type")
)

// GetInfo implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetInfo(ctx context.Context) (*Info, error) {
	return p.director.GetInfo(ctx)
}

// GetDeployments implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetDeployments(ctx context.Context) ([]Deployment, error) {
	return p.director.GetDeployments(ctx)
}

// GetDeployment implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetDeployment(ctx context.Context, name string) (*DeploymentDetail, error) {
	return p.director.GetDeployment(ctx, name)
}

// CreateDeployment implements Director interface with pooling.
func (p *PooledDirector) CreateDeployment(ctx context.Context, manifest string) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.CreateDeployment(ctx, manifest)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w for CreateDeployment result: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// DeleteDeployment implements Director interface with pooling.
func (p *PooledDirector) DeleteDeployment(ctx context.Context, name string) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.DeleteDeployment(ctx, name)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w for DeleteDeployment result: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// GetDeploymentVMs implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]VM, error) {
	return p.director.GetDeploymentVMs(ctx, deployment)
}

// GetReleases implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetReleases(ctx context.Context) ([]Release, error) {
	return p.director.GetReleases(ctx)
}

// UploadRelease implements Director interface with pooling.
func (p *PooledDirector) UploadRelease(ctx context.Context, url string, sha1 string) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UploadRelease(ctx, url, sha1)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w for UploadRelease result: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// GetStemcells implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetStemcells(ctx context.Context) ([]Stemcell, error) {
	return p.director.GetStemcells(ctx)
}

// UploadStemcell implements Director interface with pooling.
func (p *PooledDirector) UploadStemcell(ctx context.Context, url string, sha1 string) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UploadStemcell(ctx, url, sha1)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// GetTask implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTask(ctx context.Context, id int) (*Task, error) {
	return p.director.GetTask(ctx, id)
}

// GetTasks implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTasks(ctx context.Context, taskType string, limit int, states []string, team string) ([]Task, error) {
	return p.director.GetTasks(ctx, taskType, limit, states, team)
}

// GetAllTasks implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetAllTasks(ctx context.Context, limit int) ([]Task, error) {
	return p.director.GetAllTasks(ctx, limit)
}

// CancelTask implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) CancelTask(ctx context.Context, taskID int) error {
	return p.director.CancelTask(ctx, taskID)
}

// WaitForTask implements Director interface - no pooling. A wait is a series
// of task reads, and the pool holds no slot for a read, so a wait holds none
// however long the task runs.
func (p *PooledDirector) WaitForTask(ctx context.Context, id int) (*Task, error) {
	return p.director.WaitForTask(ctx, id)
}

// GetTaskOutput implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTaskOutput(ctx context.Context, id int, outputType string) (string, error) {
	return p.director.GetTaskOutput(ctx, id, outputType)
}

// GetTaskEvents implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTaskEvents(ctx context.Context, id int) ([]TaskEvent, error) {
	return p.director.GetTaskEvents(ctx, id)
}

// GetEvents implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetEvents(ctx context.Context, deployment string) ([]Event, error) {
	return p.director.GetEvents(ctx, deployment)
}

// UpdateCloudConfig implements Director interface with pooling.
func (p *PooledDirector) UpdateCloudConfig(ctx context.Context, config string) error {
	return p.withConnection(ctx, func() error {
		return p.director.UpdateCloudConfig(ctx, config)
	})
}

// GetCloudConfig implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetCloudConfig(ctx context.Context) (string, error) {
	return p.director.GetCloudConfig(ctx)
}

// ListCloudConfigs implements Director interface - no pooling for read operations.
func (p *PooledDirector) ListCloudConfigs(ctx context.Context) ([]CloudConfigEntry, error) {
	entries, err := p.director.ListCloudConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cloud configs: %w", err)
	}

	return entries, nil
}

// GetConfigs implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigs(ctx context.Context, limit int, configTypes []string) ([]BoshConfig, error) {
	return p.director.GetConfigs(ctx, limit, configTypes)
}

// GetConfigVersions implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigVersions(ctx context.Context, configType, name string, limit int) ([]BoshConfig, error) {
	return p.director.GetConfigVersions(ctx, configType, name, limit)
}

// GetConfigByID implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigByID(ctx context.Context, configID string) (*BoshConfigDetail, error) {
	return p.director.GetConfigByID(ctx, configID)
}

// GetConfigContent implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigContent(ctx context.Context, configID string) (string, error) {
	return p.director.GetConfigContent(ctx, configID)
}

// GetConfig implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfig(ctx context.Context, configType, configName string) (interface{}, error) {
	return p.director.GetConfig(ctx, configType, configName)
}

// ComputeConfigDiff implements Director interface - no pooling for read operations.
func (p *PooledDirector) ComputeConfigDiff(ctx context.Context, fromID, toID string) (*ConfigDiff, error) {
	return p.director.ComputeConfigDiff(ctx, fromID, toID)
}

// Cleanup implements Director interface with pooling.
func (p *PooledDirector) Cleanup(ctx context.Context, removeAll bool) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.Cleanup(ctx, removeAll)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// FetchLogs implements Director interface - no pooling for read operations.
func (p *PooledDirector) FetchLogs(ctx context.Context, deployment string, jobName string, jobIndex string) (string, error) {
	return p.director.FetchLogs(ctx, deployment, jobName, jobIndex)
}

// SSHCommand implements Director interface - no pooling for read operations.
func (p *PooledDirector) SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return p.director.SSHCommand(ctx, deployment, instance, index, command, args, options)
}

// SSHSession implements Director interface - no pooling for read operations.
func (p *PooledDirector) SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return p.director.SSHSession(ctx, deployment, instance, index, options)
}

// EnableResurrection implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) EnableResurrection(ctx context.Context, deployment string, enabled bool) error {
	return p.director.EnableResurrection(ctx, deployment, enabled)
}

// DeleteResurrectionConfig implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) DeleteResurrectionConfig(ctx context.Context, deploymentName string) error {
	return p.director.DeleteResurrectionConfig(ctx, deploymentName)
}

// RestartDeployment implements Director interface with pooling.
func (p *PooledDirector) RestartDeployment(ctx context.Context, name string, opts RestartOpts) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RestartDeployment(ctx, name, opts)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// StopDeployment implements Director interface with pooling.
func (p *PooledDirector) StopDeployment(ctx context.Context, name string, opts StopOpts) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.StopDeployment(ctx, name, opts)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// StartDeployment implements Director interface with pooling.
func (p *PooledDirector) StartDeployment(ctx context.Context, name string, opts StartOpts) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.StartDeployment(ctx, name, opts)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// RecreateDeployment implements Director interface with pooling.
func (p *PooledDirector) RecreateDeployment(ctx context.Context, name string, opts RecreateOpts) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RecreateDeployment(ctx, name, opts)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// ListErrands implements Director interface - no pooling for read operations.
func (p *PooledDirector) ListErrands(ctx context.Context, deployment string) ([]Errand, error) {
	return p.director.ListErrands(ctx, deployment)
}

// RunErrand implements Director interface with pooling.
func (p *PooledDirector) RunErrand(ctx context.Context, deployment, errand string, opts ErrandOpts) (*ErrandResult, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RunErrand(ctx, deployment, errand, opts)
	})
	if err != nil {
		return nil, err
	}

	errandResult, ok := result.(*ErrandResult)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return errandResult, nil
}

// GetInstances implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetInstances(ctx context.Context, deployment string) ([]Instance, error) {
	return p.director.GetInstances(ctx, deployment)
}

// UpdateDeployment implements Director interface with pooling.
func (p *PooledDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error) {
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UpdateDeployment(ctx, name, manifest)
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return task, nil
}

// FindRunningTaskForDeployment implements Director interface - no pooling for read operations.
func (p *PooledDirector) FindRunningTaskForDeployment(ctx context.Context, deploymentName string) (*Task, error) {
	return p.director.FindRunningTaskForDeployment(ctx, deploymentName)
}

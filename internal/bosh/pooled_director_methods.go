package bosh

import (
	"context"
	"errors"
	"fmt"
)

// Constants for pooled director operations.
const (
	// Upload operations require more time.
	uploadTimeoutMultiplier = 3

	// Timeout multiplier for long-running operations.
	longRunningTimeoutMultiplier = 2
)

// Static error variables to satisfy err113.
var (
	ErrUnexpectedResultType = errors.New("unexpected result type")
)

// GetInfo implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetInfo() (*Info, error) {
	return p.director.GetInfo()
}

// GetDeployments implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetDeployments() ([]Deployment, error) {
	return p.director.GetDeployments()
}

// GetDeployment implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetDeployment(name string) (*DeploymentDetail, error) {
	return p.director.GetDeployment(name)
}

// CreateDeployment implements Director interface with pooling.
func (p *PooledDirector) CreateDeployment(manifest string) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.CreateDeployment(manifest)
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
func (p *PooledDirector) DeleteDeployment(name string) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.DeleteDeployment(name)
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
func (p *PooledDirector) GetDeploymentVMs(deployment string) ([]VM, error) {
	return p.director.GetDeploymentVMs(deployment)
}

// GetReleases implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetReleases() ([]Release, error) {
	return p.director.GetReleases()
}

// UploadRelease implements Director interface with pooling.
func (p *PooledDirector) UploadRelease(url string, sha1 string) (*Task, error) {
	// Use longer timeout for upload operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*uploadTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UploadRelease(url, sha1)
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
func (p *PooledDirector) GetStemcells() ([]Stemcell, error) {
	return p.director.GetStemcells()
}

// UploadStemcell implements Director interface with pooling.
func (p *PooledDirector) UploadStemcell(url string, sha1 string) (*Task, error) {
	// Use longer timeout for upload operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*uploadTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UploadStemcell(url, sha1)
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
func (p *PooledDirector) GetTask(id int) (*Task, error) {
	return p.director.GetTask(id)
}

// GetTasks implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTasks(taskType string, limit int, states []string, team string) ([]Task, error) {
	return p.director.GetTasks(taskType, limit, states, team)
}

// GetAllTasks implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetAllTasks(limit int) ([]Task, error) {
	return p.director.GetAllTasks(limit)
}

// CancelTask implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) CancelTask(taskID int) error {
	return p.director.CancelTask(taskID)
}

// GetTaskOutput implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTaskOutput(id int, outputType string) (string, error) {
	return p.director.GetTaskOutput(id, outputType)
}

// GetTaskEvents implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetTaskEvents(id int) ([]TaskEvent, error) {
	return p.director.GetTaskEvents(id)
}

// GetEvents implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetEvents(deployment string) ([]Event, error) {
	return p.director.GetEvents(deployment)
}

// UpdateCloudConfig implements Director interface with pooling.
func (p *PooledDirector) UpdateCloudConfig(config string) error {
	// Use longer timeout for config update operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	return p.withConnection(ctx, func() error {
		return p.director.UpdateCloudConfig(config)
	})
}

// GetCloudConfig implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetCloudConfig() (string, error) {
	return p.director.GetCloudConfig()
}

// GetConfigs implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigs(limit int, configTypes []string) ([]BoshConfig, error) {
	return p.director.GetConfigs(limit, configTypes)
}

// GetConfigVersions implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error) {
	return p.director.GetConfigVersions(configType, name, limit)
}

// GetConfigByID implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigByID(configID string) (*BoshConfigDetail, error) {
	return p.director.GetConfigByID(configID)
}

// GetConfigContent implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfigContent(configID string) (string, error) {
	return p.director.GetConfigContent(configID)
}

// GetConfig implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetConfig(configType, configName string) (interface{}, error) {
	return p.director.GetConfig(configType, configName)
}

// ComputeConfigDiff implements Director interface - no pooling for read operations.
func (p *PooledDirector) ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error) {
	return p.director.ComputeConfigDiff(fromID, toID)
}

// Cleanup implements Director interface with pooling.
func (p *PooledDirector) Cleanup(removeAll bool) (*Task, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.Cleanup(removeAll)
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
func (p *PooledDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	return p.director.FetchLogs(deployment, jobName, jobIndex)
}

// SSHCommand implements Director interface - no pooling for read operations.
func (p *PooledDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return p.director.SSHCommand(deployment, instance, index, command, args, options)
}

// SSHSession implements Director interface - no pooling for read operations.
func (p *PooledDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return p.director.SSHSession(deployment, instance, index, options)
}

// EnableResurrection implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) EnableResurrection(deployment string, enabled bool) error {
	return p.director.EnableResurrection(deployment, enabled)
}

// DeleteResurrectionConfig implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) DeleteResurrectionConfig(deploymentName string) error {
	return p.director.DeleteResurrectionConfig(deploymentName)
}

// RestartDeployment implements Director interface with pooling.
func (p *PooledDirector) RestartDeployment(name string, opts RestartOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RestartDeployment(name, opts)
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
func (p *PooledDirector) StopDeployment(name string, opts StopOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.StopDeployment(name, opts)
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
func (p *PooledDirector) StartDeployment(name string, opts StartOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.StartDeployment(name, opts)
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
func (p *PooledDirector) RecreateDeployment(name string, opts RecreateOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*uploadTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RecreateDeployment(name, opts)
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
func (p *PooledDirector) ListErrands(deployment string) ([]Errand, error) {
	return p.director.ListErrands(deployment)
}

// RunErrand implements Director interface with pooling.
func (p *PooledDirector) RunErrand(deployment, errand string, opts ErrandOpts) (*ErrandResult, error) {
	// Use longer timeout for errand operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*uploadTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RunErrand(deployment, errand, opts)
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
func (p *PooledDirector) GetInstances(deployment string) ([]Instance, error) {
	return p.director.GetInstances(deployment)
}

// UpdateDeployment implements Director interface with pooling.
func (p *PooledDirector) UpdateDeployment(name, manifest string) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UpdateDeployment(name, manifest)
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
func (p *PooledDirector) FindRunningTaskForDeployment(deploymentName string) (*Task, error) {
	return p.director.FindRunningTaskForDeployment(deploymentName)
}

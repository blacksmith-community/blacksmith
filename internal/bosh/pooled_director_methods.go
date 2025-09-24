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

// GetInfo implements Director interface with pooling.
func (p *PooledDirector) GetInfo() (*Info, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetInfo()
	})
	if err != nil {
		return nil, err
	}

	info, ok := result.(*Info)
	if !ok {
		return nil, fmt.Errorf("%w for GetInfo result: %T", ErrUnexpectedResultType, result)
	}

	return info, nil
}

// GetDeployments implements Director interface with pooling.
func (p *PooledDirector) GetDeployments() ([]Deployment, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetDeployments()
	})
	if err != nil {
		return nil, err
	}

	deployments, ok := result.([]Deployment)
	if !ok {
		return nil, fmt.Errorf("%w for GetDeployments result: %T", ErrUnexpectedResultType, result)
	}

	return deployments, nil
}

// GetDeployment implements Director interface with pooling.
func (p *PooledDirector) GetDeployment(name string) (*DeploymentDetail, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetDeployment(name)
	})
	if err != nil {
		return nil, err
	}

	deployment, ok := result.(*DeploymentDetail)
	if !ok {
		return nil, fmt.Errorf("%w for GetDeployment result: %T", ErrUnexpectedResultType, result)
	}

	return deployment, nil
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

// GetDeploymentVMs implements Director interface with pooling.
func (p *PooledDirector) GetDeploymentVMs(deployment string) ([]VM, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetDeploymentVMs(deployment)
	})
	if err != nil {
		return nil, err
	}

	vms, ok := result.([]VM)
	if !ok {
		return nil, fmt.Errorf("%w for GetDeploymentVMs result: %T", ErrUnexpectedResultType, result)
	}

	return vms, nil
}

// GetReleases implements Director interface with pooling.
func (p *PooledDirector) GetReleases() ([]Release, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetReleases()
	})
	if err != nil {
		return nil, err
	}

	releases, ok := result.([]Release)
	if !ok {
		return nil, fmt.Errorf("%w for GetReleases result: %T", ErrUnexpectedResultType, result)
	}

	return releases, nil
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

// GetStemcells implements Director interface with pooling.
func (p *PooledDirector) GetStemcells() ([]Stemcell, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetStemcells()
	})
	if err != nil {
		return nil, err
	}

	stemcells, ok := result.([]Stemcell)
	if !ok {
		return nil, fmt.Errorf("%w for GetStemcells result: %T", ErrUnexpectedResultType, result)
	}

	return stemcells, nil
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

// GetTask implements Director interface with pooling.
func (p *PooledDirector) GetTask(id int) (*Task, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTask(id)
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

// GetTasks implements Director interface with pooling.
func (p *PooledDirector) GetTasks(taskType string, limit int, states []string, team string) ([]Task, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTasks(taskType, limit, states, team)
	})
	if err != nil {
		return nil, err
	}

	tasks, ok := result.([]Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return tasks, nil
}

// GetAllTasks implements Director interface with pooling.
func (p *PooledDirector) GetAllTasks(limit int) ([]Task, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetAllTasks(limit)
	})
	if err != nil {
		return nil, err
	}

	tasks, ok := result.([]Task)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return tasks, nil
}

// CancelTask implements Director interface with pooling.
func (p *PooledDirector) CancelTask(taskID int) error {
	ctx := context.Background()

	return p.withConnection(ctx, func() error {
		return p.director.CancelTask(taskID)
	})
}

// GetTaskOutput implements Director interface with pooling.
func (p *PooledDirector) GetTaskOutput(id int, outputType string) (string, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTaskOutput(id, outputType)
	})
	if err != nil {
		return "", err
	}

	output, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return output, nil
}

// GetTaskEvents implements Director interface with pooling.
func (p *PooledDirector) GetTaskEvents(id int) ([]TaskEvent, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTaskEvents(id)
	})
	if err != nil {
		return nil, err
	}

	events, ok := result.([]TaskEvent)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return events, nil
}

// GetEvents implements Director interface with pooling.
func (p *PooledDirector) GetEvents(deployment string) ([]Event, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetEvents(deployment)
	})
	if err != nil {
		return nil, err
	}

	events, ok := result.([]Event)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return events, nil
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

// GetCloudConfig implements Director interface with pooling.
func (p *PooledDirector) GetCloudConfig() (string, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetCloudConfig()
	})
	if err != nil {
		return "", err
	}

	str, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return str, nil
}

// GetConfigs implements Director interface with pooling.
func (p *PooledDirector) GetConfigs(limit int, configTypes []string) ([]BoshConfig, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigs(limit, configTypes)
	})
	if err != nil {
		return nil, err
	}

	configs, ok := result.([]BoshConfig)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return configs, nil
}

// GetConfigVersions implements Director interface with pooling.
func (p *PooledDirector) GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigVersions(configType, name, limit)
	})
	if err != nil {
		return nil, err
	}

	configs, ok := result.([]BoshConfig)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return configs, nil
}

// GetConfigByID implements Director interface with pooling.
func (p *PooledDirector) GetConfigByID(configID string) (*BoshConfigDetail, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigByID(configID)
	})
	if err != nil {
		return nil, err
	}

	config, ok := result.(*BoshConfigDetail)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return config, nil
}

// GetConfigContent implements Director interface with pooling.
func (p *PooledDirector) GetConfigContent(configID string) (string, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigContent(configID)
	})
	if err != nil {
		return "", err
	}

	str, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return str, nil
}

// GetConfig implements Director interface with pooling.
func (p *PooledDirector) GetConfig(configType, configName string) (interface{}, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfig(configType, configName)
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ComputeConfigDiff implements Director interface with pooling.
func (p *PooledDirector) ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.ComputeConfigDiff(fromID, toID)
	})
	if err != nil {
		return nil, err
	}

	diff, ok := result.(*ConfigDiff)
	if !ok {
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return diff, nil
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

// FetchLogs implements Director interface with pooling.
func (p *PooledDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	// Use longer timeout for log fetching operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.FetchLogs(deployment, jobName, jobIndex)
	})
	if err != nil {
		return "", err
	}

	str, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return str, nil
}

// SSHCommand implements Director interface with pooling.
func (p *PooledDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	// SSH operations may take longer
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*longRunningTimeoutMultiplier)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.SSHCommand(deployment, instance, index, command, args, options)
	})
	if err != nil {
		return "", err
	}

	str, ok := result.(string)
	if !ok {
		return "", fmt.Errorf("%w: %T", ErrUnexpectedResultType, result)
	}

	return str, nil
}

// SSHSession implements Director interface with pooling.
func (p *PooledDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.SSHSession(deployment, instance, index, options)
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// EnableResurrection implements Director interface with pooling.
func (p *PooledDirector) EnableResurrection(deployment string, enabled bool) error {
	ctx := context.Background()

	return p.withConnection(ctx, func() error {
		return p.director.EnableResurrection(deployment, enabled)
	})
}

// DeleteResurrectionConfig implements Director interface with pooling.
func (p *PooledDirector) DeleteResurrectionConfig(deploymentName string) error {
	ctx := context.Background()

	return p.withConnection(ctx, func() error {
		return p.director.DeleteResurrectionConfig(deploymentName)
	})
}

// RestartDeployment implements Director interface with pooling
func (p *PooledDirector) RestartDeployment(name string, opts RestartOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RestartDeployment(name, opts)
	})

	if err != nil {
		return nil, err
	}

	return result.(*Task), nil
}

// StopDeployment implements Director interface with pooling
func (p *PooledDirector) StopDeployment(name string, opts StopOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.StopDeployment(name, opts)
	})

	if err != nil {
		return nil, err
	}

	return result.(*Task), nil
}

// StartDeployment implements Director interface with pooling
func (p *PooledDirector) StartDeployment(name string, opts StartOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.StartDeployment(name, opts)
	})

	if err != nil {
		return nil, err
	}

	return result.(*Task), nil
}

// RecreateDeployment implements Director interface with pooling
func (p *PooledDirector) RecreateDeployment(name string, opts RecreateOpts) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*3)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RecreateDeployment(name, opts)
	})

	if err != nil {
		return nil, err
	}

	return result.(*Task), nil
}

// ListErrands implements Director interface with pooling
func (p *PooledDirector) ListErrands(deployment string) ([]Errand, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.ListErrands(deployment)
	})

	if err != nil {
		return nil, err
	}

	return result.([]Errand), nil
}

// RunErrand implements Director interface with pooling
func (p *PooledDirector) RunErrand(deployment, errand string, opts ErrandOpts) (*ErrandResult, error) {
	// Use longer timeout for errand operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*3)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.RunErrand(deployment, errand, opts)
	})

	if err != nil {
		return nil, err
	}

	return result.(*ErrandResult), nil
}

// GetInstances implements Director interface with pooling
func (p *PooledDirector) GetInstances(deployment string) ([]Instance, error) {
	ctx := context.Background()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetInstances(deployment)
	})

	if err != nil {
		return nil, err
	}

	return result.([]Instance), nil
}

// UpdateDeployment implements Director interface with pooling
func (p *PooledDirector) UpdateDeployment(name, manifest string) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UpdateDeployment(name, manifest)
	})

	if err != nil {
		return nil, err
	}

	return result.(*Task), nil
}

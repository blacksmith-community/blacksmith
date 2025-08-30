package bosh

import (
	"context"
)

// GetInfo implements Director interface with pooling
func (p *PooledDirector) GetInfo() (*Info, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetInfo()
	})

	if err != nil {
		return nil, err
	}
	return result.(*Info), nil
}

// GetDeployments implements Director interface with pooling
func (p *PooledDirector) GetDeployments() ([]Deployment, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetDeployments()
	})

	if err != nil {
		return nil, err
	}
	return result.([]Deployment), nil
}

// GetDeployment implements Director interface with pooling
func (p *PooledDirector) GetDeployment(name string) (*DeploymentDetail, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetDeployment(name)
	})

	if err != nil {
		return nil, err
	}
	return result.(*DeploymentDetail), nil
}

// CreateDeployment implements Director interface with pooling
func (p *PooledDirector) CreateDeployment(manifest string) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.CreateDeployment(manifest)
	})

	if err != nil {
		return nil, err
	}
	return result.(*Task), nil
}

// DeleteDeployment implements Director interface with pooling
func (p *PooledDirector) DeleteDeployment(name string) (*Task, error) {
	// Use longer timeout for deployment operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.DeleteDeployment(name)
	})

	if err != nil {
		return nil, err
	}
	return result.(*Task), nil
}

// GetDeploymentVMs implements Director interface with pooling
func (p *PooledDirector) GetDeploymentVMs(deployment string) ([]VM, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetDeploymentVMs(deployment)
	})

	if err != nil {
		return nil, err
	}
	return result.([]VM), nil
}

// GetReleases implements Director interface with pooling
func (p *PooledDirector) GetReleases() ([]Release, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetReleases()
	})

	if err != nil {
		return nil, err
	}
	return result.([]Release), nil
}

// UploadRelease implements Director interface with pooling
func (p *PooledDirector) UploadRelease(url string, sha1 string) (*Task, error) {
	// Use longer timeout for upload operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*3)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UploadRelease(url, sha1)
	})

	if err != nil {
		return nil, err
	}
	return result.(*Task), nil
}

// GetStemcells implements Director interface with pooling
func (p *PooledDirector) GetStemcells() ([]Stemcell, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetStemcells()
	})

	if err != nil {
		return nil, err
	}
	return result.([]Stemcell), nil
}

// UploadStemcell implements Director interface with pooling
func (p *PooledDirector) UploadStemcell(url string, sha1 string) (*Task, error) {
	// Use longer timeout for upload operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*3)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.UploadStemcell(url, sha1)
	})

	if err != nil {
		return nil, err
	}
	return result.(*Task), nil
}

// GetTask implements Director interface with pooling
func (p *PooledDirector) GetTask(id int) (*Task, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTask(id)
	})

	if err != nil {
		return nil, err
	}
	return result.(*Task), nil
}

// GetTasks implements Director interface with pooling
func (p *PooledDirector) GetTasks(taskType string, limit int, states []string, team string) ([]Task, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTasks(taskType, limit, states, team)
	})

	if err != nil {
		return nil, err
	}
	return result.([]Task), nil
}

// GetAllTasks implements Director interface with pooling
func (p *PooledDirector) GetAllTasks(limit int) ([]Task, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetAllTasks(limit)
	})

	if err != nil {
		return nil, err
	}
	return result.([]Task), nil
}

// CancelTask implements Director interface with pooling
func (p *PooledDirector) CancelTask(taskID int) error {
	ctx := context.Background()
	return p.withConnection(ctx, func() error {
		return p.director.CancelTask(taskID)
	})
}

// GetTaskOutput implements Director interface with pooling
func (p *PooledDirector) GetTaskOutput(id int, outputType string) (string, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTaskOutput(id, outputType)
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// GetTaskEvents implements Director interface with pooling
func (p *PooledDirector) GetTaskEvents(id int) ([]TaskEvent, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetTaskEvents(id)
	})

	if err != nil {
		return nil, err
	}
	return result.([]TaskEvent), nil
}

// GetEvents implements Director interface with pooling
func (p *PooledDirector) GetEvents(deployment string) ([]Event, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetEvents(deployment)
	})

	if err != nil {
		return nil, err
	}
	return result.([]Event), nil
}

// UpdateCloudConfig implements Director interface with pooling
func (p *PooledDirector) UpdateCloudConfig(config string) error {
	// Use longer timeout for config update operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	return p.withConnection(ctx, func() error {
		return p.director.UpdateCloudConfig(config)
	})
}

// GetCloudConfig implements Director interface with pooling
func (p *PooledDirector) GetCloudConfig() (string, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetCloudConfig()
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// GetConfigs implements Director interface with pooling
func (p *PooledDirector) GetConfigs(limit int, configTypes []string) ([]BoshConfig, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigs(limit, configTypes)
	})

	if err != nil {
		return nil, err
	}
	return result.([]BoshConfig), nil
}

// GetConfigVersions implements Director interface with pooling
func (p *PooledDirector) GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigVersions(configType, name, limit)
	})

	if err != nil {
		return nil, err
	}
	return result.([]BoshConfig), nil
}

// GetConfigByID implements Director interface with pooling
func (p *PooledDirector) GetConfigByID(configID string) (*BoshConfigDetail, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigByID(configID)
	})

	if err != nil {
		return nil, err
	}
	return result.(*BoshConfigDetail), nil
}

// GetConfigContent implements Director interface with pooling
func (p *PooledDirector) GetConfigContent(configID string) (string, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.GetConfigContent(configID)
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// GetConfig implements Director interface with pooling
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

// ComputeConfigDiff implements Director interface with pooling
func (p *PooledDirector) ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.ComputeConfigDiff(fromID, toID)
	})

	if err != nil {
		return nil, err
	}
	return result.(*ConfigDiff), nil
}

// Cleanup implements Director interface with pooling
func (p *PooledDirector) Cleanup(removeAll bool) (*Task, error) {
	ctx := context.Background()
	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.Cleanup(removeAll)
	})

	if err != nil {
		return nil, err
	}
	return result.(*Task), nil
}

// FetchLogs implements Director interface with pooling
func (p *PooledDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	// Use longer timeout for log fetching operations
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.FetchLogs(deployment, jobName, jobIndex)
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// SSHCommand implements Director interface with pooling
func (p *PooledDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	// SSH operations may take longer
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout*2)
	defer cancel()

	result, err := p.withConnectionResult(ctx, func() (interface{}, error) {
		return p.director.SSHCommand(deployment, instance, index, command, args, options)
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// SSHSession implements Director interface with pooling
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

// EnableResurrection implements Director interface with pooling
func (p *PooledDirector) EnableResurrection(deployment string, enabled bool) error {
	ctx := context.Background()
	return p.withConnection(ctx, func() error {
		return p.director.EnableResurrection(deployment, enabled)
	})
}

// DeleteResurrectionConfig implements Director interface with pooling
func (p *PooledDirector) DeleteResurrectionConfig(deploymentName string) error {
	ctx := context.Background()
	return p.withConnection(ctx, func() error {
		return p.director.DeleteResurrectionConfig(deploymentName)
	})
}

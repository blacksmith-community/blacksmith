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

// CreateDeployment implements Director interface with pooling. The deploy
// holds its slot until its task ends. A manifest that names no deployment
// is refused before a slot is taken.
func (p *PooledDirector) CreateDeployment(ctx context.Context, manifest string) (*Task, error) {
	name := ExtractDeploymentName(manifest)
	if name == "" {
		return nil, fmt.Errorf("%w: the manifest names no deployment", ErrDeploymentNameExtractionFailed)
	}

	return p.followMutation(ctx, poolDeployFollow(name), func(ctx context.Context) (*Task, error) {
		return p.director.CreateDeployment(ctx, manifest)
	})
}

// DeleteDeployment implements Director interface with pooling. The delete
// holds its slot until its task ends.
func (p *PooledDirector) DeleteDeployment(ctx context.Context, name string) (*Task, error) {
	return p.followMutation(ctx, follow{kind: "delete", deployment: name, lookup: contextIDLookup{match: deleteMatch(name)}}, func(ctx context.Context) (*Task, error) {
		return p.director.DeleteDeployment(ctx, name)
	})
}

// GetDeploymentVMs implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]VM, error) {
	return p.director.GetDeploymentVMs(ctx, deployment)
}

// GetReleases implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetReleases(ctx context.Context) ([]Release, error) {
	return p.director.GetReleases(ctx)
}

// UploadRelease implements Director interface with pooling. The upload
// holds its slot until its task ends.
func (p *PooledDirector) UploadRelease(ctx context.Context, url string, sha1 string) (*Task, error) {
	return p.followMutation(ctx, follow{kind: "release upload", lookup: graceHold{}}, func(ctx context.Context) (*Task, error) {
		return p.director.UploadRelease(ctx, url, sha1)
	})
}

// GetStemcells implements Director interface - no pooling for read operations.
func (p *PooledDirector) GetStemcells(ctx context.Context) ([]Stemcell, error) {
	return p.director.GetStemcells(ctx)
}

// UploadStemcell implements Director interface with pooling. The upload
// holds its slot until its task ends.
func (p *PooledDirector) UploadStemcell(ctx context.Context, url string, sha1 string) (*Task, error) {
	return p.followMutation(ctx, follow{kind: "stemcell upload", lookup: graceHold{}}, func(ctx context.Context) (*Task, error) {
		return p.director.UploadStemcell(ctx, url, sha1)
	})
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

// WaitForTask implements Director interface - no pooling. A wait for a task
// the pool follows gets the follow's final task, so it never reads the
// director alongside the follower. A wait for any other task passes
// through. A wait holds no slot however long the task runs.
func (p *PooledDirector) WaitForTask(ctx context.Context, id int) (*Task, error) {
	task, followed, err := p.follow.awaitTask(ctx, id)
	if followed {
		return task, err
	}

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

// Cleanup implements Director interface with pooling. The cleanup holds
// its slot until its task ends.
func (p *PooledDirector) Cleanup(ctx context.Context, removeAll bool) (*Task, error) {
	return p.followMutation(ctx, follow{kind: "cleanup", lookup: graceHold{}}, func(ctx context.Context) (*Task, error) {
		return p.director.Cleanup(ctx, removeAll)
	})
}

// FetchLogs implements Director interface - no pooling for read operations.
func (p *PooledDirector) FetchLogs(ctx context.Context, deployment string, jobName string, instanceID string, budget *LogBudget) (*LogBundle, error) {
	return p.director.FetchLogs(ctx, deployment, jobName, instanceID, budget) //nolint:wrapcheck // the wrapped director's own error
}

// SSHCommand implements Director interface - no pooling for read operations.
func (p *PooledDirector) SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return p.director.SSHCommand(ctx, deployment, instance, index, command, args, options)
}

// SSHSession implements Director interface - no pooling for read operations.
func (p *PooledDirector) SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return p.director.SSHSession(ctx, deployment, instance, index, options)
}

// SetSSHCleanupStore hands the wrapped director the store its SSH setups
// record their users in. A director that keeps no records is told
// nothing, and the pool logs that at Error.
func (p *PooledDirector) SetSSHCleanupStore(store SSHCleanupStore) {
	owner, ok := p.director.(sshCleanupOwner)
	if !ok {
		p.logger.Errorf("The pooled director's BOSH director (%T) keeps no SSH cleanup records, so a missed SSH cleanup is never retried", p.director)

		return
	}

	owner.SetSSHCleanupStore(store)
}

// SweepSSHCleanups runs the wrapped director's sweep of its SSH cleanup
// records. The sweep takes no pool slot, because each cleanup it sends
// waits for its own task in the call. A director that keeps no records
// returns ErrSSHCleanupsUnsupported.
func (p *PooledDirector) SweepSSHCleanups(ctx context.Context) error {
	owner, ok := p.director.(sshCleanupOwner)
	if !ok {
		return fmt.Errorf("%w: the pooled director wraps %T", ErrSSHCleanupsUnsupported, p.director)
	}

	return owner.SweepSSHCleanups(ctx) //nolint:wrapcheck // the wrapped director's own error
}

// ShutdownSSH hands the shutdown of SSH to the wrapped director, which
// sends the cleanup of every open session within ctx. It takes no pool
// slot, because the pool may be full while the broker stops. A director
// that cannot do this is told nothing, and the pool logs that at Error.
func (p *PooledDirector) ShutdownSSH(ctx context.Context) {
	owner, ok := p.director.(sshShutdowner)
	if !ok {
		p.logger.Errorf("The pooled director's BOSH director (%T) cannot send its SSH cleanups at shutdown, so the user of each open session is left to its record",
			p.director)

		return
	}

	owner.ShutdownSSH(ctx)
}

// EnableResurrection implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) EnableResurrection(ctx context.Context, deployment string, enabled bool) error {
	return p.director.EnableResurrection(ctx, deployment, enabled)
}

// DeleteResurrectionConfig implements Director interface - no pooling (doesn't create a task).
func (p *PooledDirector) DeleteResurrectionConfig(ctx context.Context, deploymentName string) error {
	return p.director.DeleteResurrectionConfig(ctx, deploymentName)
}

// RestartDeployment implements Director interface with pooling. The
// restart holds its slot until its task ends.
func (p *PooledDirector) RestartDeployment(ctx context.Context, name string, opts RestartOpts) (*Task, error) {
	return p.followMutation(ctx, jobStateFollow("restart", name), func(ctx context.Context) (*Task, error) {
		return p.director.RestartDeployment(ctx, name, opts)
	})
}

// StopDeployment implements Director interface with pooling. The
// stop holds its slot until its task ends.
func (p *PooledDirector) StopDeployment(ctx context.Context, name string, opts StopOpts) (*Task, error) {
	return p.followMutation(ctx, jobStateFollow("stop", name), func(ctx context.Context) (*Task, error) {
		return p.director.StopDeployment(ctx, name, opts)
	})
}

// StartDeployment implements Director interface with pooling. The
// start holds its slot until its task ends.
func (p *PooledDirector) StartDeployment(ctx context.Context, name string, opts StartOpts) (*Task, error) {
	return p.followMutation(ctx, jobStateFollow("start", name), func(ctx context.Context) (*Task, error) {
		return p.director.StartDeployment(ctx, name, opts)
	})
}

// RecreateDeployment implements Director interface with pooling. The
// recreate holds its slot until its task ends.
func (p *PooledDirector) RecreateDeployment(ctx context.Context, name string, opts RecreateOpts) (*Task, error) {
	return p.followMutation(ctx, jobStateFollow("recreate", name), func(ctx context.Context) (*Task, error) {
		return p.director.RecreateDeployment(ctx, name, opts)
	})
}

// ListErrands implements Director interface - no pooling for read operations.
func (p *PooledDirector) ListErrands(ctx context.Context, deployment string) ([]Errand, error) {
	return p.director.ListErrands(ctx, deployment)
}

// RunErrand implements Director interface with pooling. An errand returns
// no task to follow, so it holds its slot for the call only, and when the
// caller's context ends early, the slot frees while the errand runs on.
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

// UpdateDeployment implements Director interface with pooling. The deploy
// holds its slot until its task ends.
func (p *PooledDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error) {
	return p.followMutation(ctx, poolDeployFollow(name), func(ctx context.Context) (*Task, error) {
		return p.director.UpdateDeployment(ctx, name, manifest)
	})
}

// FindRunningTaskForDeployment implements Director interface - no pooling for read operations.
func (p *PooledDirector) FindRunningTaskForDeployment(ctx context.Context, deploymentName string) (*Task, error) {
	return p.director.FindRunningTaskForDeployment(ctx, deploymentName)
}

// FindRunningTasksForDeployment implements Director interface - no pooling for read operations.
func (p *PooledDirector) FindRunningTasksForDeployment(ctx context.Context, deploymentName string) ([]Task, error) {
	tasks, err := p.director.FindRunningTasksForDeployment(ctx, deploymentName)
	if err != nil {
		return nil, fmt.Errorf("list the running tasks of deployment %s: %w", deploymentName, err)
	}

	return tasks, nil
}

// FindTaskByContextID implements Director interface - no pooling for read operations.
func (p *PooledDirector) FindTaskByContextID(ctx context.Context, contextID string) (*Task, error) {
	task, err := p.director.FindTaskByContextID(ctx, contextID)
	if err != nil {
		return nil, fmt.Errorf("find the task with context ID %s: %w", contextID, err)
	}

	return task, nil
}

// poolDeployFollow is the follow of a deploy of the deployment. Unlike the
// batch director's, it is not supersedable, so a later deploy never takes
// over its slot.
func poolDeployFollow(deployment string) follow {
	return follow{kind: kindDeploy, deployment: deployment, lookup: contextIDLookup{match: deployMatch(deployment)}}
}

// jobStateFollow is the follow of a job-state change on the deployment. The
// director may store no context ID for it, and its task can't be told
// apart from others on the deployment, so a lost answer holds the slot
// while any task runs there.
func jobStateFollow(kind, deployment string) follow {
	return follow{kind: kind, deployment: deployment, lookup: runningTaskLookup{match: anyRunning()}}
}

# Blacksmith VM Monitor

The VM Monitor is a critical component of the Blacksmith service broker that provides continuous health monitoring of BOSH-deployed service instances.
It tracks VM status, performs health checks, and maintains state information for all managed services.

## Overview

The VM Monitor operates as a background service that:
- **Discovers Services**: Automatically scans Vault index for service instances
- **Health Monitoring**: Periodically checks VM health via BOSH Director API
- **Adaptive Scheduling**: Uses different intervals for healthy vs failed services
- **Status Storage**: Maintains detailed VM status information in Vault
- **API Integration**: Exposes VM health data through internal management API

## Architecture

### Core Components

#### VMMonitor (`vm_monitor.go:11-25`)
Main monitoring service managing the entire VM monitoring lifecycle:

```go
type VMMonitor struct {
    vault        *Vault           // Vault client for data storage
    boshDirector bosh.Director    // BOSH director for VM queries
    config       *Config          // Global configuration

    normalInterval time.Duration  // Check interval for healthy services
    failedInterval time.Duration  // Check interval for failed services

    services map[string]*ServiceMonitor  // Tracked services
    mu       sync.RWMutex                // Thread-safe access

    ctx    context.Context     // Lifecycle context
    cancel context.CancelFunc  // Cancellation function
    wg     sync.WaitGroup      // Wait group for shutdown
}
```

#### ServiceMonitor (`vm_monitor.go:28-36`)
Individual service tracking state:

```go
type ServiceMonitor struct {
    ServiceID      string    // Blacksmith service instance ID
    DeploymentName string    // BOSH deployment name (planID-instanceID)
    LastStatus     string    // Last known VM status
    LastCheck      time.Time // Timestamp of last health check
    NextCheck      time.Time // Scheduled next check time
    FailureCount   int       // Consecutive failure count
    IsHealthy      bool      // Current health status
}
```

#### VMStatus (`vm_monitor.go:39-47`)
VM health status data structure:

```go
type VMStatus struct {
    Status      string                 // Overall status (running, failing, etc.)
    VMCount     int                    // Total number of VMs
    HealthyVMs  int                    // Number of healthy VMs
    LastUpdated time.Time              // Timestamp of last update
    NextUpdate  time.Time              // Scheduled next update
    VMs         []bosh.VM              // Detailed VM information
    Details     map[string]interface{} // Additional status details
}
```

### VM Data Structure (`bosh/adapter.go:90-128`)

The monitor works with VM data from BOSH:

```go
type VM struct {
    // Core VM identity
    ID      string    // VM unique identifier
    AgentID string    // BOSH agent ID
    CID     string    // Cloud provider VM ID

    // Job information
    Job      string   // BOSH job name
    Index    int      // Job instance index
    JobState string   // Job aggregate state (running, failing, etc.)

    // VM state and lifecycle
    State                    string    // VM power state
    Active                   *bool     // Active status
    Bootstrap                bool      // Bootstrap node indicator
    Ignore                   bool      // Skip monitoring if true
    ResurrectionPaused       bool      // Resurrection disabled
    ResurrectionConfigExists bool      // Resurrection configuration present
    VMCreatedAt              time.Time // VM creation timestamp

    // Network and placement
    IPs []string  // Assigned IP addresses
    DNS []string  // DNS entries
    AZ  string    // Availability zone

    // Resource allocation
    VMType       string  // VM type/flavor
    ResourcePool string  // Resource pool assignment

    // Storage
    DiskCID  string    // Primary disk cloud ID
    DiskCIDs []string  // All attached disk IDs

    // Detailed monitoring data
    CloudProperties interface{} // Cloud-specific properties
    Processes       []VMProcess // Running processes
    Vitals          VMVitals    // System vitals (CPU, memory, disk)
    Stemcell        VMStemcell  // Stemcell information
}
```

## Configuration

### VM Monitoring Configuration (`config.go:70-77`)

```yaml
vm_monitoring:
  enabled: true              # Enable/disable VM monitoring (pointer type)
  normal_interval: 3600      # Check interval for healthy services (seconds)
  failed_interval: 300       # Check interval for failed services (seconds)
  max_retries: 3            # Maximum retry attempts before marking as failed
  timeout: 30               # Health check timeout (seconds)
  max_concurrent: 5         # Maximum concurrent health checks
```

### Configuration Defaults (`vm_monitor.go:50-74`)

When not explicitly configured, the monitor uses these defaults:
- **Normal Interval**: 1 hour (3600 seconds)
- **Failed Interval**: 5 minutes (300 seconds)
- **Concurrency**: 3 concurrent health checks
- **Retry Logic**: Exponential backoff for failed checks

### Example Configurations

#### Basic Configuration
```yaml
vm_monitoring:
  enabled: true
  normal_interval: 1800    # 30 minutes for healthy services
  failed_interval: 180     # 3 minutes for failed services
```

#### Production Configuration
```yaml
vm_monitoring:
  enabled: true
  normal_interval: 900     # 15 minutes for healthy services
  failed_interval: 120     # 2 minutes for failed services
  max_retries: 5
  timeout: 60
  max_concurrent: 10
```

#### Development Configuration
```yaml
vm_monitoring:
  enabled: true
  normal_interval: 300     # 5 minutes for faster feedback
  failed_interval: 60      # 1 minute for rapid failure detection
  max_concurrent: 3
```

## Workflow and Operations

### Initialization Process (`vm_monitor.go:77-104`)

1. **Configuration Validation**
   - Check if monitoring is enabled via `config.VMMonitoring.Enabled`
   - Set interval defaults if not configured
   - Initialize service tracking maps

2. **Service Discovery** (`vm_monitor.go:119-158`)
   - Query Vault `db` index for all service instances
   - Extract `plan_id` and `instance_id` for each service
   - Generate deployment names using pattern: `{planID}-{instanceID}`
   - Create `ServiceMonitor` entries for each discovered service

3. **Monitor Loop Startup**
   - Start background monitoring goroutine
   - Schedule initial health checks for all services
   - Begin periodic checking cycle (1-minute intervals)

### Health Monitoring Loop (`vm_monitor.go:161-179`)

The core monitoring operates on a continuous loop:

```
┌─────────────────┐
│ 1-minute ticker │
└─────┬───────────┘
      │
      ▼
┌─────────────────────┐
│ Check services due  │
│ for monitoring      │
└─────┬───────────────┘
      │
      ▼
┌─────────────────────┐
│ Concurrent health   │
│ checks (max 3)      │
└─────┬───────────────┘
      │
      ▼
┌─────────────────────┐
│ Update schedules    │
│ based on results    │
└─────────────────────┘
```

### Health Check Process (`vm_monitor.go:222-270`)

For each service requiring monitoring:

1. **VM Data Retrieval**
   - Query BOSH Director via `GetDeploymentVMs(deploymentName)`
   - Retrieve complete VM information including vitals and processes
   - Handle connection errors and timeouts

2. **Status Calculation** (`vm_monitor.go:303-331`)
   - Analyze `JobState` for each VM (primary health indicator)
   - Apply status priority ranking:
     - `failing` (priority 1 - highest concern)
     - `unresponsive` (priority 2)
     - `stopping` (priority 3)
     - `starting` (priority 4)
     - `stopped` (priority 5)
     - `running` (priority 6 - healthy)
   - Set overall service status to worst individual VM status

3. **Health Metrics** (`vm_monitor.go:334-347`)
   - Count VMs with `JobState == "running"`
   - Calculate healthy VM percentage
   - Log detailed VM state information

4. **Adaptive Scheduling**
   - **Healthy Services**: Next check scheduled using `normalInterval`
   - **Failed Services**: Next check scheduled using `failedInterval`
   - **Error Handling**: Failed checks increment failure count and use failed interval

5. **Data Persistence** (`vm_monitor.go:350-366`)
   - Store `VMStatus` in Vault at path: `{serviceID}/vm_status`
   - Include timestamp information for trend analysis
   - Store detailed VM information for debugging

### Error Handling (`vm_monitor.go:273-300`)

When health checks fail:

1. **Error Classification**
   - Network connectivity issues
   - BOSH Director authentication failures
   - Deployment not found errors
   - VM query timeouts

2. **Failure Tracking**
   - Increment `FailureCount` for affected service
   - Mark service as unhealthy (`IsHealthy = false`)
   - Schedule retry using `failedInterval`

3. **Error Status Storage**
   - Store error details in Vault
   - Include failure count and error message
   - Maintain error history for troubleshooting

## Integration with System Components

### Vault Integration

**Data Storage Paths**:
- `{serviceID}/vm_status` - Current VM health status
- `db` index - Service instance discovery and metadata

**Stored Information**:
```json
{
  "status": "running",
  "vm_count": 3,
  "healthy_vms": 3,
  "last_updated": 1640995200,
  "next_update": 1640998800,
  "vms": [...],
  "details": {...}
}
```

### BOSH Director Integration

**API Operations**:
- `GetDeploymentVMs(deploymentName)` - Retrieve VM information
- Connection pooling via `PooledDirector`
- Authentication using configured BOSH credentials

**Retrieved VM Data**:
- VM identity and cloud properties
- Job states and process information
- Resource utilization vitals
- Network configuration and IPs
- Disk allocation and usage

### Internal API Integration (`internal_api.go:535-542`)

VM status data is exposed through the internal management API:

```go
// Instance listing includes VM status
if vmStatus, err := api.VMMonitor.GetServiceVMStatus(instanceID); err == nil && vmStatus != nil {
    enrichedInstanceMap["vm_status"] = vmStatus.Status
    enrichedInstanceMap["vm_count"] = vmStatus.VMCount
    enrichedInstanceMap["vm_healthy"] = vmStatus.HealthyVMs
}
```

## Operational Features

### Manual Refresh Operations

#### Individual Service Refresh (`vm_monitor.go:397-410`)
```go
func (m *VMMonitor) TriggerRefresh(serviceID string) error
```
- Immediately schedules health check for specific service
- Updates `NextCheck` to current time
- Useful for on-demand health verification

#### Global Refresh (`vm_monitor.go:413-425`)
```go
func (m *VMMonitor) TriggerRefreshAll()
```
- Triggers immediate health check for all monitored services
- Forces re-evaluation of all service states
- Useful after system maintenance or configuration changes

### Status Retrieval (`vm_monitor.go:369-394`)

```go
func (m *VMMonitor) GetServiceVMStatus(serviceID string) (*VMStatus, error)
```
- Retrieves current VM status from Vault
- Returns structured `VMStatus` with timing information
- Used by API endpoints for status reporting

### Lifecycle Management

#### Startup Sequence (`main.go:391, 418-429`)
1. VMMonitor initialization with Vault, BOSH Director, and config
2. Service discovery from Vault index
3. Background monitoring loop startup
4. Initial health check trigger (2-second delay)

#### Shutdown Sequence (`main.go:424-428`)
1. Context cancellation signal
2. Graceful monitoring loop termination
3. Wait for ongoing health checks to complete
4. Resource cleanup and connection closure

## Health Status States

### VM Job States (Primary Health Indicator)

Based on BOSH Director VM information:
- **`running`**: VM and all processes are healthy and operational
- **`failing`**: One or more processes have failed or are unhealthy
- **`unresponsive`**: VM agent is not responding to director queries
- **`stopping`**: VM is in the process of being stopped
- **`starting`**: VM is in the process of starting up
- **`stopped`**: VM is intentionally stopped

### Service Health Calculation

Overall service health is determined by the worst VM state:
- If any VM is `failing` → Service status: `failing`
- If any VM is `unresponsive` → Service status: `unresponsive`
- If all VMs are `running` → Service status: `running`

### Health Metrics

- **VM Count**: Total number of VMs in deployment
- **Healthy VM Count**: VMs with `JobState == "running"`
- **Health Percentage**: `HealthyVMs / VMCount * 100`

## Monitoring Intervals and Scheduling

### Adaptive Interval Strategy

The monitor uses different check frequencies based on service health:

#### Healthy Services (Normal Interval)
- **Default**: 1 hour (3600 seconds)
- **Rationale**: Healthy services require less frequent monitoring
- **Configuration**: `vm_monitoring.normal_interval`

#### Failed Services (Failed Interval)
- **Default**: 5 minutes (300 seconds)
- **Rationale**: Failed services need frequent monitoring for recovery detection
- **Configuration**: `vm_monitoring.failed_interval`

#### Check Scheduling Logic
```
if service.status != "running":
    next_check = now + failed_interval
    failure_count++
else:
    next_check = now + normal_interval
    failure_count = 0
```

### Concurrency Management

- **Maximum Concurrent Checks**: 3 (hardcoded in `checkScheduledServices`)
- **Semaphore-Based Limiting**: Prevents BOSH Director overload
- **Goroutine Management**: Each health check runs in separate goroutine
- **Wait Group Coordination**: Ensures all checks complete before scheduling cycle ends

## Troubleshooting and Operations

### Common Issues

#### Monitoring Disabled
**Symptom**: No VM status updates in Vault
**Check**: `config.VMMonitoring.Enabled` configuration
**Solution**: Set `vm_monitoring.enabled: true` in configuration

#### Connection Failures
**Symptom**: All services showing "error" status
**Check**: BOSH Director connectivity and credentials
**Logs**: Look for "Failed to check VMs" error messages
**Solution**: Verify BOSH configuration and network connectivity

#### High Failure Counts
**Symptom**: Services showing persistent failure counts
**Check**: VM actual health vs. monitoring configuration
**Solution**: Adjust `failed_interval` or investigate underlying VM issues

#### Performance Issues
**Symptom**: Slow monitoring updates or timeouts
**Check**: Concurrent check limits and BOSH Director performance
**Solution**: Tune `max_concurrent` and monitoring intervals

### Monitoring Best Practices

#### Interval Configuration
- **Production**: `normal_interval: 900` (15 min), `failed_interval: 120` (2 min)
- **Development**: `normal_interval: 300` (5 min), `failed_interval: 60` (1 min)
- **High-Density**: Increase intervals to reduce BOSH Director load

#### Health Check Tuning
- Monitor BOSH Director performance impact
- Adjust concurrency based on director capacity
- Consider service criticality for interval selection

#### Alerting Integration
- Monitor services with persistent high failure counts
- Alert on monitoring system failures (all services showing "error")
- Track trends in VM health percentages

### Debugging and Diagnostics

#### Log Levels
- **Info**: Service discovery, status changes, major operations
- **Debug**: Individual VM states, detailed health calculations
- **Error**: Health check failures, Vault storage issues

#### Key Log Messages
- `"Starting VM monitor with intervals: normal=%v, failed=%v"` - Startup
- `"Discovered %d service instances to monitor"` - Service discovery
- `"Checking %d services due for monitoring"` - Scheduled checks
- `"Stored VM status for %s: status=%s, healthy=%d, total=%d"` - Status updates

#### Vault Data Inspection
```bash
# View all monitored services
vault kv list secret/

# Check specific service VM status
vault kv get secret/{service-id}/vm_status

# View service discovery data
vault kv get secret/db
```

#### BOSH Director Queries
```bash
# Manual VM status check
bosh -d {deployment-name} vms --vitals

# Check VM processes
bosh -d {deployment-name} vms --ps
```

## Performance Considerations

### Resource Usage

#### Memory Footprint
- Service map scales with number of managed services
- VM data cached temporarily during health checks
- Minimal persistent memory usage

#### CPU Utilization
- Periodic background processing (1-minute ticker)
- CPU spikes during concurrent health checks
- Scales with number of services and VMs per service

#### Network Traffic
- Regular BOSH Director API calls
- Traffic proportional to VM count and check frequency
- Vault API calls for status storage

### Scaling Considerations

#### Large Deployments (100+ Services)
- Increase `normal_interval` to reduce director load
- Consider increasing concurrency limits
- Monitor BOSH Director performance impact

#### High VM Count Services
- Individual services with many VMs require longer check times
- Consider timeout adjustments for large deployments
- Monitor memory usage during VM data processing

#### Geographic Distribution
- Network latency affects check completion times
- Adjust timeout values for remote BOSH Directors
- Consider regional monitoring distribution

## Security Considerations

### Authentication and Authorization
- Uses configured BOSH Director credentials
- Vault authentication via service token
- Read-only operations on BOSH Director
- Secure credential storage in Vault

### Network Security
- All communications over TLS (when properly configured)
- BOSH Director certificate validation
- Vault TLS/certificate validation
- No sensitive data in monitoring logs

### Data Privacy
- VM vitals may contain system information
- Process lists could reveal service architecture
- Vault storage provides encryption at rest
- Monitor log levels to prevent sensitive data exposure

## Advanced Features

### Health Status Priority System

VM health calculation uses a priority-based approach to determine overall service health:

```go
statusPriority := map[string]int{
    "failing":      1,  // Highest priority (worst state)
    "unresponsive": 2,
    "stopping":     3,
    "starting":     4,
    "stopped":      5,
    "running":      6,  // Lowest priority (best state)
}
```

This ensures that any critical VM state immediately affects overall service status.

### Monitoring State Management

#### Service Discovery Process
1. Query Vault `db` index for all service instances
2. Extract `plan_id` and `instance_id` for deployment name generation
3. Create `ServiceMonitor` entries with immediate check scheduling
4. Continuous discovery updates when new services are provisioned

#### Failure Recovery Detection
- Services marked as failed get frequent checks (`failed_interval`)
- Automatic transition back to normal interval when health recovers
- Failure count reset on successful health check
- Historical failure tracking for trend analysis

### Integration Points

#### Reconciler System
- VM Monitor operates independently of reconciler
- Both systems use Vault for service discovery
- Complementary monitoring approaches (VM health vs. CF state)

#### Internal Management API
- VM status data enriches service instance listings
- Provides real-time health information to web UI
- Enables operational dashboards and reporting

#### SSH and WebSocket Services
- VM monitoring provides health context for SSH operations
- Failed VMs may indicate SSH connectivity issues
- Health status guides troubleshooting workflows

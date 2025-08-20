# Blacksmith Deployment Reconciler

## Overview

The Blacksmith Deployment Reconciler is a background service that automatically synchronizes service instance deployments between BOSH Director and Vault storage. This ensures that previously deployed services remain visible and manageable in Blacksmith even after restarts or failures.

## Purpose

When Blacksmith restarts, it may lose track of service instances that were previously deployed through BOSH. The reconciler solves this problem by:

1. Scanning all deployments from the BOSH Director
2. Matching deployments to known service plans
3. Updating Vault with deployment information
4. Maintaining the service instance index
5. Detecting orphaned instances (instances in Vault without corresponding BOSH deployments)

## Architecture

The reconciler is implemented as a modular system with clean separation of concerns:

```
┌─────────────────────────────────────────────────────┐
│                   Main Process                      │
│                                                     │
│  ┌───────────────────────────────────────────────┐ │
│  │          Reconciler Manager                   │ │
│  │                                              │ │
│  │  ┌──────────┐  ┌──────────┐  ┌───────────┐ │ │
│  │  │ Scanner  │→ │ Matcher  │→ │  Updater  │ │ │
│  │  └──────────┘  └──────────┘  └───────────┘ │ │
│  │       ↓             ↓              ↓        │ │
│  │  ┌──────────────────────────────────────┐  │ │
│  │  │         Synchronizer                 │  │ │
│  │  └──────────────────────────────────────┘  │ │
│  │                    ↓                        │ │
│  │  ┌──────────────────────────────────────┐  │ │
│  │  │      Metrics Collector               │  │ │
│  │  └──────────────────────────────────────┘  │ │
│  └───────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────┘
         ↓                    ↓              ↓
    BOSH Director          Vault         Index
```

### Components

- **Manager**: Orchestrates the reconciliation process and manages the background goroutine
- **Scanner**: Retrieves deployment information from BOSH Director with caching
- **Matcher**: Matches deployments to service instances using multiple strategies
- **Updater**: Updates Vault with deployment metadata and maintains history
- **Synchronizer**: Synchronizes the service instance index and detects orphans
- **Metrics Collector**: Tracks reconciliation metrics for monitoring

## Configuration

The reconciler can be configured through both the `blacksmith.conf` file and environment variables (environment variables take precedence):

### Configuration Options

| Environment Variable | Config File | Default | Description |
|---------------------|-------------|---------|-------------|
| `BLACKSMITH_RECONCILER_ENABLED` | `reconciler.enabled` | `true` | Enable/disable the reconciler |
| `BLACKSMITH_RECONCILER_INTERVAL` | `reconciler.interval` | `1h` | Time between reconciliation runs |
| `BLACKSMITH_RECONCILER_MAX_CONCURRENCY` | `reconciler.max_concurrency` | `5` | Maximum concurrent deployment processing |
| `BLACKSMITH_RECONCILER_BATCH_SIZE` | `reconciler.batch_size` | `10` | Number of deployments to process per batch |
| `BLACKSMITH_RECONCILER_RETRY_ATTEMPTS` | `reconciler.retry_attempts` | `3` | Number of retry attempts for failed operations |
| `BLACKSMITH_RECONCILER_RETRY_DELAY` | `reconciler.retry_delay` | `10s` | Delay between retry attempts |
| `BLACKSMITH_RECONCILER_CACHE_TTL` | `reconciler.cache_ttl` | `5m` | Cache time-to-live for deployment details |
| `BLACKSMITH_RECONCILER_BACKUP_ENABLED` | `reconciler.backup.enabled` | `true` | Enable/disable instance backups |
| `BLACKSMITH_RECONCILER_BACKUP_RETENTION` | `reconciler.backup.retention` | `10` | Number of backups to keep per instance |
| `BLACKSMITH_RECONCILER_BACKUP_CLEANUP` | `reconciler.backup.cleanup` | `true` | Enable automatic cleanup of old backups |
| `BLACKSMITH_RECONCILER_BACKUP_PATH` | `reconciler.backup.path` | `backups` | Vault path for storing backups |

### Backup Configuration

The reconciler automatically creates backups of instance data before performing updates. This provides a safety net for recovery in case of issues.

#### Backup Features
- **Automatic Backups**: Created before each reconciliation update
- **Configurable Retention**: Keep a specified number of backups per instance
- **Smart Cleanup**: Automatically removes old backups beyond retention limit
- **Timestamped Storage**: Each backup stored with Unix timestamp for easy identification

#### What Gets Backed Up
- Instance index data (service ID, plan ID, timestamps)
- Instance metadata (releases, stemcells, VMs, properties)
- Instance manifest (full BOSH deployment manifest)
- Reconciliation history

#### Backup Storage Location
Backups are stored in Vault at: `{instanceID}/{backup_path}/{unix_timestamp}`

Example: `abc-123-def-456/backups/1704397200`

### Configuration File Example

```yaml
# blacksmith.conf
reconciler:
  enabled: true
  interval: "30m"
  max_concurrency: 10
  batch_size: 20
  retry_attempts: 5
  retry_delay: "15s"
  cache_ttl: "10m"
  backup:
    enabled: true
    retention: 20
    cleanup: true
    path: "reconciler-backups"
```

### Environment Variable Example

```bash
# Basic reconciler configuration
export BLACKSMITH_RECONCILER_ENABLED=true
export BLACKSMITH_RECONCILER_INTERVAL=30m

# Performance tuning
export BLACKSMITH_RECONCILER_MAX_CONCURRENCY=10
export BLACKSMITH_RECONCILER_BATCH_SIZE=20

# Backup configuration
export BLACKSMITH_RECONCILER_BACKUP_ENABLED=true
export BLACKSMITH_RECONCILER_BACKUP_RETENTION=20
export BLACKSMITH_RECONCILER_BACKUP_CLEANUP=true
export BLACKSMITH_RECONCILER_BACKUP_PATH=backups
```

### Configuration Precedence

Configuration values are applied in the following order (highest precedence first):
1. Environment variables
2. Configuration file (`blacksmith.conf`)
3. Default values

### Disabling Backups

To disable backup creation entirely:

```bash
export BLACKSMITH_RECONCILER_BACKUP_ENABLED=false
```

Or in `blacksmith.conf`:
```yaml
reconciler:
  backup:
    enabled: false
```

### Unlimited Backup Retention

To keep all backups without automatic cleanup:

```bash
export BLACKSMITH_RECONCILER_BACKUP_CLEANUP=false
```

Or set retention to 0:
```bash
export BLACKSMITH_RECONCILER_BACKUP_RETENTION=0
```

## Matching Strategies

The reconciler uses multiple strategies to match BOSH deployments to service instances:

### 1. UUID Pattern Matching (Primary)

Deployments following the naming convention `{planID}-{instanceID}` are matched directly:

```
Example: redis-small-12345678-1234-1234-1234-123456789abc
         └─planID─┘ └──────────instanceID────────────┘
```

### 2. Manifest Metadata Matching

The reconciler checks deployment manifests for Blacksmith metadata:

```yaml
properties:
  blacksmith:
    service_id: redis-service
    plan_id: small
    instance_id: 12345678-1234-1234-1234-123456789abc
```

### 3. Release-Based Matching

Deployments are matched based on their BOSH releases:

- Redis deployments: Contains `redis` release
- PostgreSQL deployments: Contains `postgres` release
- RabbitMQ deployments: Contains `rabbitmq` release

### 4. Pattern Recognition

The reconciler can identify services through deployment name patterns and manifest structure when other methods fail.

## Reconciliation Process

The reconciliation process follows these steps:

### 1. Initialization
- Reconciler starts as a background goroutine on Blacksmith startup
- Performs initial reconciliation immediately
- Schedules periodic reconciliation based on configured interval

### 2. Scanning Phase
```go
// Retrieve all deployments from BOSH
deployments := scanner.ScanDeployments()

// Filter service deployments
serviceDeployments := filterServiceDeployments(deployments)
```

### 3. Matching Phase
```go
// Match each deployment to a service instance
for deployment := range serviceDeployments {
    match := matcher.MatchDeployment(deployment)
    if match != nil {
        matched = append(matched, match)
    }
}
```

### 4. Update Phase
```go
// Update Vault with deployment information
for match := range matched {
    instance := buildInstanceData(match)
    updater.UpdateInstance(instance)
}
```

### 5. Synchronization Phase
```go
// Synchronize the index
synchronizer.SyncIndex(instances)

// Detect and mark orphaned instances
orphans := detectOrphans(vaultInstances, boshDeployments)
markOrphaned(orphans)
```

## Orphan Detection

The reconciler identifies "orphaned" instances - service instances that exist in Vault but have no corresponding BOSH deployment:

```go
// Instance marked as orphaned
{
  "id": "12345678-1234-1234-1234-123456789abc",
  "service_id": "redis-service",
  "plan_id": "small",
  "orphaned": true,
  "orphaned_at": "2024-01-15T10:30:00Z"
}
```

Orphaned instances may indicate:
- Failed deployments that need cleanup
- Manual BOSH deployment deletions
- Incomplete deprovision operations

## Metrics and Monitoring

The reconciler collects metrics for monitoring:

```go
type Metrics struct {
    TotalRuns            int64         // Total reconciliation runs
    SuccessfulRuns       int64         // Successful runs
    FailedRuns           int64         // Failed runs
    TotalDuration        time.Duration // Total time spent reconciling
    AverageDuration      time.Duration // Average reconciliation duration
    LastRunDuration      time.Duration // Duration of last run
    TotalDeployments     int64         // Total deployments scanned
    TotalInstancesFound  int64         // Total instances matched
    TotalInstancesSynced int64         // Total instances synchronized
    TotalErrors          int64         // Total errors encountered
}
```

### Accessing Metrics

Metrics can be accessed through the reconciler status:

```go
status := reconciler.GetStatus()
metrics := reconciler.GetMetrics()
```

## Logging

The reconciler provides comprehensive logging at different levels:

### Debug Logging
Enable debug logging for detailed reconciliation information:

```bash
export BLACKSMITH_DEBUG=true
# or
export DEBUG=true
```

Debug logs include:
- Deployment scanning details
- Matching decisions and confidence scores
- Cache hits/misses
- Individual update operations

### Info Logging
Standard operational logs:
- Reconciliation start/completion
- Number of deployments found and matched
- Synchronization results

### Error Logging
Critical issues that require attention:
- BOSH Director connection failures
- Vault access errors
- Matching failures for critical deployments

## Error Handling

The reconciler implements robust error handling:

### Retry Logic
Failed operations are retried with exponential backoff:
```go
for attempt := 1; attempt <= maxRetries; attempt++ {
    err := operation()
    if err == nil {
        break
    }
    time.Sleep(retryDelay * time.Duration(attempt))
}
```

### Partial Failure Handling
The reconciler continues processing even when individual deployments fail:
- Failed deployments are logged but don't stop the reconciliation
- Metrics track failed operations for monitoring
- Next reconciliation attempt will retry failed deployments

### Graceful Shutdown
The reconciler supports graceful shutdown:
```go
// Stop reconciler gracefully
reconciler.Stop()
// Waits for current reconciliation to complete
// Cancels scheduled reconciliations
// Cleans up resources
```

## Performance Considerations

### Caching
Deployment details are cached to reduce BOSH API calls:
- Default TTL: 5 minutes
- Cache is cleared on forced reconciliation
- Stale cache entries are automatically purged

### Concurrency Control
The reconciler processes deployments concurrently with limits:
- Default: 5 concurrent operations
- Prevents overwhelming BOSH Director
- Configurable based on deployment size

### Batch Processing
Large deployment lists are processed in batches:
- Default batch size: 10 deployments
- Reduces memory usage
- Enables progress tracking

## Troubleshooting

### Reconciler Not Starting

Check if reconciler is enabled:
```bash
echo $BLACKSMITH_RECONCILER_ENABLED
```

Check logs for initialization errors:
```bash
grep "reconciler" blacksmith.log
```

### Deployments Not Being Found

Enable debug logging to see scanning details:
```bash
export BLACKSMITH_DEBUG=true
```

Verify BOSH Director connectivity:
```bash
bosh deployments
```

### Matching Failures

Check deployment naming conventions:
- Should follow `{planID}-{instanceID}` pattern
- Instance ID should be a valid UUID

Verify manifest metadata:
```bash
bosh -d <deployment-name> manifest | grep blacksmith
```

### Performance Issues

Adjust concurrency settings:
```bash
# Reduce concurrency for slower systems
export BLACKSMITH_RECONCILER_MAX_CONCURRENCY=2
export BLACKSMITH_RECONCILER_BATCH_SIZE=5

# Increase cache TTL to reduce API calls
export BLACKSMITH_RECONCILER_CACHE_TTL=15m
```

### Force Reconciliation

To trigger immediate reconciliation:
```go
// Programmatically
reconciler.ForceReconcile()
```

## Integration with Blacksmith

The reconciler integrates seamlessly with existing Blacksmith components:

### Broker Integration
- Reads service catalog for matching
- Updates service instance registry
- Maintains compatibility with OSB API

### Vault Integration
- Stores deployment metadata securely
- Maintains instance history
- Preserves credentials and parameters

### BOSH Integration
- Uses existing BOSH Director connection
- Respects BOSH authentication settings
- Compatible with all BOSH deployment types

## Best Practices

1. **Regular Monitoring**: Check reconciler metrics regularly to ensure healthy operation

2. **Appropriate Intervals**: Set reconciliation interval based on deployment frequency:
   - High-frequency deployments: 15-30 minutes
   - Low-frequency deployments: 1-2 hours
   - Development environments: 5-10 minutes

3. **Resource Allocation**: Ensure adequate resources for reconciliation:
   - CPU: Minimal impact during normal operation
   - Memory: Scales with number of deployments
   - Network: Depends on BOSH API response size

4. **Error Investigation**: Investigate orphaned instances promptly:
   - May indicate deployment issues
   - Could reveal incomplete operations
   - Helps maintain clean state

5. **Cache Management**: Tune cache settings for your environment:
   - Larger deployments: Longer TTL
   - Rapidly changing environments: Shorter TTL
   - Production: Balance between performance and freshness

## Security Considerations

The reconciler operates with the same security context as Blacksmith:

- **BOSH Access**: Uses configured BOSH credentials
- **Vault Access**: Uses Blacksmith's Vault token
- **No Additional Permissions**: Doesn't require extra privileges
- **Secure Metadata**: Sensitive information stored in Vault
- **Audit Trail**: All operations logged for compliance

## Future Enhancements

Planned improvements for the reconciler:

1. **Web UI Integration**: Dashboard showing reconciliation status
2. **Webhook Notifications**: Alert on orphaned instances
3. **Custom Matching Rules**: User-defined matching strategies
4. **Selective Reconciliation**: Reconcile specific services only
5. **Prometheus Metrics**: Direct metrics export
6. **Historical Tracking**: Long-term reconciliation history
7. **Auto-remediation**: Automatic cleanup of orphaned instances
8. **Multi-BOSH Support**: Reconcile across multiple directors
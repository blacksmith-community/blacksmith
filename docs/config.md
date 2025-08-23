# Blacksmith Configuration Reference

This document provides comprehensive documentation for all Blacksmith configuration file options. The configuration file is in YAML format and defines how Blacksmith connects to external services and operates.

## Configuration File Structure

The main configuration file contains the following top-level sections:

```yaml
broker:       # HTTP server and authentication settings
vault:        # Vault integration configuration
shield:       # SHIELD backup integration (optional)
bosh:         # BOSH director configuration
services:     # Service-specific behavior configuration
vm_monitoring: # VM monitoring and status tracking (optional)
reconciler:   # Deployment reconciler configuration (optional)
debug:        # Enable debug logging
web-root:     # Static web content directory
env:          # Environment identifier
shareable:    # Enable shareable service instances
forges:       # Service template discovery configuration
```

## Top-Level Configuration Options

### `debug` (boolean, optional)
**Default:** `false`

Enable debug logging for detailed troubleshooting information.

```yaml
debug: true
```

### `web-root` (string, optional)
**Default:** `""` (disabled)

Path to directory containing static web content to serve via the web UI. If not specified, web UI assets are served from embedded files.

```yaml
web-root: "/path/to/web/assets"
```

### `env` (string, optional)
**Default:** `""`

Environment identifier used for labeling and organization purposes. Displayed in the web UI and used for internal identification.

```yaml
env: "production"
```

### `shareable` (boolean, optional)
**Default:** `false`

Enable shareable service instances, allowing multiple applications to bind to the same service instance.

```yaml
shareable: true
```

## Services Configuration (`services`)

The `services` section configures service-specific behavior and operational settings.

```yaml
services:
  skip_tls_verify:
    - rabbitmq
    - redis
```

### Optional Fields

#### `skip_tls_verify` (array of strings)
**Default:** `[]`

List of service types for which TLS certificate verification should be skipped when Blacksmith connects to service APIs. This is useful when services are deployed with self-signed certificates or certificates that don't include proper IP Subject Alternative Names (SANs).

**Supported values:**
- Service names: `"rabbitmq"`, `"redis"`, `"postgres"`, etc.
- `"all"`: Skip TLS verification for all services

**Security Warning:** Only use this setting in development environments or when you understand the security implications. Skipping TLS verification makes connections vulnerable to man-in-the-middle attacks.

**Examples:**

Skip TLS verification for specific services:
```yaml
services:
  skip_tls_verify:
    - rabbitmq
    - redis
```

Skip TLS verification for all services (not recommended for production):
```yaml
services:
  skip_tls_verify:
    - all
```

**Use Cases:**
- RabbitMQ deployments with IP-based certificates that lack proper IP SANs
- Redis deployments with self-signed certificates
- Development environments with locally generated certificates
- Services behind load balancers that terminate TLS with different certificates

## VM Monitoring Configuration (`vm_monitoring`)

The `vm_monitoring` section configures automatic monitoring of BOSH VM health for service instances, providing real-time status visibility in the Blacksmith web UI.

```yaml
vm_monitoring:
  enabled: true              # Enable VM monitoring
  normal_interval: 3600      # Check healthy deployments every hour
  failed_interval: 300       # Check unhealthy deployments every 5 minutes
  max_retries: 3            # Maximum retry attempts
  timeout: 30               # BOSH command timeout (seconds)
  max_concurrent: 3         # Maximum concurrent VM checks
```

### Optional Fields

#### `enabled` (boolean)
**Default:** `true`

Enable or disable VM monitoring. When enabled, Blacksmith will periodically fetch VM status from BOSH and display color-coded status badges in the service instance list.

#### `normal_interval` (integer)
**Default:** `3600` (1 hour)

Interval in seconds between VM health checks for deployments with all VMs running normally. This helps minimize BOSH API load for healthy services.

#### `failed_interval` (integer)
**Default:** `300` (5 minutes)

Interval in seconds between VM health checks for deployments with failing or unhealthy VMs. Faster checking enables quicker detection of recovery.

#### `max_retries` (integer)
**Default:** `3`

Maximum number of retry attempts for failed BOSH API calls before marking a service as having an error state.

#### `timeout` (integer)
**Default:** `30`

Timeout in seconds for individual BOSH CLI commands when fetching VM information.

#### `max_concurrent` (integer)
**Default:** `3`

Maximum number of concurrent VM health checks to prevent overwhelming the BOSH director with simultaneous API calls.

### VM Status Display

When VM monitoring is enabled, the Blacksmith web UI displays color-coded status badges for each service instance:

- **Green (running)**: All VMs are running normally
- **Red (failing/unresponsive)**: One or more VMs are failing or unresponsive
- **Yellow (starting/stopping)**: VMs are in transition states
- **Gray (stopped/unknown)**: VMs are stopped or status cannot be determined

Status badges show VM health ratios (e.g., "3/4" indicating 3 healthy VMs out of 4 total) and are clickable to view detailed VM information.

### Performance Considerations

- VM status data is cached in Vault to provide fast UI response times
- Monitoring intervals can be adjusted based on environment needs and BOSH director capacity
- The background worker uses controlled concurrency to prevent API overload
- Failed checks automatically use shorter intervals for faster recovery detection

## Broker Configuration (`broker`)

The `broker` section configures the HTTP server, authentication, and operational timeouts.

```yaml
broker:
  username: "blacksmith"        # HTTP basic auth username
  password: "blacksmith"        # HTTP basic auth password
  port: "3000"                  # HTTP server port
  bind_ip: "0.0.0.0"           # IP address to bind to
  read_timeout: 120             # HTTP read timeout (seconds)
  write_timeout: 120            # HTTP write timeout (seconds)
  idle_timeout: 300             # HTTP idle timeout (seconds)
  tls:                          # TLS configuration (see TLS section)
    enabled: true
    port: "443"
    certificate: "/path/to/cert.pem"
    key: "/path/to/key.pem"
```

### Required Fields
- None (all fields have defaults)

### Optional Fields

#### `username` (string)
**Default:** `"blacksmith"`

Username for HTTP basic authentication. Used by Cloud Foundry to authenticate with the service broker.

#### `password` (string)
**Default:** `"blacksmith"`

Password for HTTP basic authentication. Should be changed from default in production environments.

#### `port` (string)
**Default:** `"3000"`

Port number for the HTTP server to listen on.

#### `bind_ip` (string)
**Default:** `"0.0.0.0"`

IP address for the HTTP server to bind to. Use `"127.0.0.1"` to restrict to localhost only.

#### `read_timeout` (integer)
**Default:** `120`

HTTP server read timeout in seconds. Maximum time allowed for reading the entire request, including the body.

#### `write_timeout` (integer)
**Default:** `120`

HTTP server write timeout in seconds. Maximum time allowed for writing the response.

#### `idle_timeout` (integer)
**Default:** `300`

HTTP server idle timeout in seconds. Maximum time to wait for the next request when keep-alives are enabled.

#### `tls` (TLSConfig)
**Default:** See TLS Configuration section

TLS/HTTPS configuration for secure connections. For detailed TLS configuration options, see [docs/tls.md](./tls.md).

## Vault Configuration (`vault`)

The `vault` section configures integration with HashiCorp Vault for secrets management.

```yaml
vault:
  address: "https://vault.example.com:8200"  # Required
  token: "vault-token"                       # Optional if using credentials file
  skip_ssl_validation: false                 # Optional
  credentials: "/path/to/vault/creds"        # Optional
```

### Required Fields

#### `address` (string)
**Required**

URL of the Vault server including protocol and port.

```yaml
vault:
  address: "https://vault.example.com:8200"
```

### Optional Fields

#### `token` (string)
**Default:** `""` (uses credentials file or environment)

Vault authentication token. If not provided, Blacksmith will attempt to read credentials from the file specified in the `credentials` field.

#### `skip_ssl_validation` (boolean)
**Default:** `false`

Skip SSL certificate validation when connecting to Vault. Should only be used in development environments.

#### `credentials` (string)
**Default:** `""`

Path to file containing Vault credentials. Used when `token` is not provided directly in the configuration.

## BOSH Configuration (`bosh`)

The `bosh` section configures integration with the BOSH director for service deployment management.

```yaml
bosh:
  address: "https://bosh.example.com:25555"  # Required
  username: "admin"                          # Required
  password: "admin-password"                 # Required
  skip_ssl_validation: false                 # Optional
  cacert: "/path/to/ca.pem"                 # Optional
  cloud-config: "/path/to/cloud-config.yml" # Optional
  network: "blacksmith"                      # Optional
  stemcells:                                 # Optional
    - name: "bosh-warden-boshlite-ubuntu-trusty-go_agent"
      version: "3468.21"
      url: "https://bosh.io/d/stemcells/bosh-warden-boshlite-ubuntu-trusty-go_agent"
      sha1: "1234567890abcdef"
  releases:                                  # Optional
    - name: "redis"
      version: "14.0.1"
      url: "https://bosh.io/d/github.com/cloudfoundry-community/redis-boshrelease"
      sha1: "abcdef1234567890"
```

### Required Fields

#### `address` (string)
**Required**

URL of the BOSH director including protocol and port.

#### `username` (string)
**Required**

Username for BOSH director authentication.

#### `password` (string)
**Required**

Password for BOSH director authentication.

### Optional Fields

#### `skip_ssl_validation` (boolean)
**Default:** `false`

Skip SSL certificate validation when connecting to BOSH director. Should only be used in development environments.

#### `cacert` (string)
**Default:** `""`

Path to CA certificate file for validating the BOSH director's SSL certificate.

#### `cloud-config` (string)
**Default:** `""`

Path to BOSH cloud config file. If provided, Blacksmith will upload this cloud config to the BOSH director at startup.

#### `network` (string)
**Default:** `"blacksmith"`

Default network name to use for deployed services. This network must be defined in the BOSH cloud config.

#### `stemcells` (array of Uploadable)
**Default:** `[]`

List of stemcells to automatically upload to the BOSH director at startup. See [Uploadable Configuration](#uploadable-configuration) for field details.

#### `releases` (array of Uploadable)
**Default:** `[]`

List of BOSH releases to automatically upload to the BOSH director at startup. See [Uploadable Configuration](#uploadable-configuration) for field details.

## SHIELD Configuration (`shield`)

The `shield` section configures optional integration with SHIELD for backup and restore functionality.

```yaml
shield:
  enabled: true                              # Required to enable SHIELD
  address: "https://shield.example.com"      # Required when enabled
  skip_ssl_validation: false                 # Optional
  agent: "shield-agent"                      # Required when enabled
  auth_method: "token"                       # Required: "token" or "local"
  token: "shield-token"                      # Required for token auth
  username: "shield-user"                    # Required for local auth
  password: "shield-password"                # Required for local auth
  tenant: "tenant-uuid-or-name"              # Required when enabled
  store: "store-uuid-or-name"                # Required when enabled
  schedule: "daily 6am"                      # Optional
  retain: "7d"                               # Optional
  enabled_on_targets: ["redis", "postgres"] # Optional
```

### Required Fields (when enabled)

#### `enabled` (boolean)
**Required**

Enable SHIELD integration. Must be `true` to use SHIELD features.

#### `address` (string)
**Required when enabled**

URL of the SHIELD server.

#### `agent` (string)
**Required when enabled**

SHIELD agent identifier for backup operations.

#### `auth_method` (string)
**Required when enabled**

Authentication method: `"token"` or `"local"`.

#### `tenant` (string)
**Required when enabled**

SHIELD tenant UUID or exact name.

#### `store` (string)
**Required when enabled**

SHIELD store UUID or exact name where backups will be stored.

### Authentication Fields

For token authentication (`auth_method: "token"`):

#### `token` (string)
**Required for token auth**

SHIELD authentication token.

For local authentication (`auth_method: "local"`):

#### `username` (string)
**Required for local auth**

SHIELD username.

#### `password` (string)
**Required for local auth**

SHIELD password.

### Optional Fields

#### `skip_ssl_validation` (boolean)
**Default:** `false`

Skip SSL certificate validation when connecting to SHIELD.

#### `schedule` (string)
**Default:** `"daily 6am"`

Backup schedule specification (e.g., "daily", "weekly", "daily at 11:00").

#### `retain` (string)
**Default:** `"7d"`

Backup retention policy (e.g., "7d", "4w", "6m").

#### `enabled_on_targets` (array of strings)
**Default:** `[]`

List of service types that should have backup enabled automatically (e.g., `["redis", "postgres", "mysql"]`).

## Forges Configuration (`forges`)

The `forges` section configures automatic discovery of service templates (forges).

```yaml
forges:
  auto-scan: true                            # Enable automatic scanning
  scan-paths: ["/path/to/forges"]           # Directories to scan
  scan-patterns: ["*-forge", "*-service"]   # Directory name patterns
```

### Optional Fields

#### `auto-scan` (boolean)
**Default:** `false`

Enable automatic scanning for service forge directories.

#### `scan-paths` (array of strings)
**Default:** `[]`

List of directory paths to scan for service forges when auto-scan is enabled.

#### `scan-patterns` (array of strings)
**Default:** `[]`

List of directory name patterns to match when scanning for service forges (supports shell-style wildcards).

## Uploadable Configuration

The `Uploadable` type is used for both stemcells and releases in the BOSH configuration:

```yaml
- name: "resource-name"           # Required
  version: "1.0.0"               # Required
  url: "https://example.com/..."  # Required
  sha1: "abc123..."              # Required
```

### Required Fields

#### `name` (string)
**Required**

Name of the stemcell or release.

#### `version` (string)
**Required**

Version of the stemcell or release.

#### `url` (string)
**Required**

Download URL for the stemcell or release.

#### `sha1` (string)
**Required**

SHA1 checksum for integrity verification.

## TLS Configuration

For detailed TLS configuration options including certificates, protocols, and ciphers, see [docs/tls.md](./tls.md).

## Environment Variables

Blacksmith sets the following environment variables based on configuration:

- `BOSH_NETWORK`: Set to the value of `bosh.network`
- `VAULT_ADDR`: Set to the value of `vault.address`
- `BOSH_CLIENT`: Set to the value of `bosh.username`
- `BOSH_CLIENT_SECRET`: Set to the value of `bosh.password`

## Reconciler Configuration (`reconciler`)

The `reconciler` section configures the deployment reconciler that monitors and maintains service instance state consistency between Vault and BOSH.

```yaml
reconciler:
  enabled: true                    # Enable reconciler
  interval: "5m"                   # Reconciliation interval
  max_concurrency: 5              # Maximum concurrent reconciliations
  batch_size: 10                  # Batch size for processing instances
  retry_attempts: 3               # Retry attempts for failed operations
  retry_delay: "30s"              # Delay between retry attempts
  cache_ttl: "5m"                 # Cache TTL for instance data
  debug: false                    # Enable reconciler debug logging
  backup:                         # Backup configuration
    enabled: true                 # Enable backup functionality
    retention_count: 5            # Number of backups to retain
    retention_days: 0             # Max age of backups in days (0 = disabled)
    compression_level: 9          # Gzip compression level (1-9)
    cleanup_enabled: true         # Enable automatic cleanup of old backups
    backup_on_update: true        # Create backup before instance updates
    backup_on_delete: true        # Create backup before instance deletion
```

### Optional Fields

#### `enabled` (boolean)
**Default:** `true`

Enable or disable the deployment reconciler. When enabled, Blacksmith will monitor service instances and ensure consistency between Vault metadata and actual BOSH deployments.

#### `interval` (string)
**Default:** `"5m"`

How often the reconciler runs to check for inconsistencies. Supports duration formats like `"30s"`, `"5m"`, `"1h"`.

#### `max_concurrency` (integer)
**Default:** `5`

Maximum number of service instances that can be reconciled simultaneously.

#### `batch_size` (integer)
**Default:** `10`

Number of instances to process in each reconciliation batch.

#### `retry_attempts` (integer)
**Default:** `3`

Number of retry attempts for failed reconciliation operations.

#### `retry_delay` (string)
**Default:** `"30s"`

Delay between retry attempts for failed operations.

#### `cache_ttl` (string)
**Default:** `"5m"`

Time-to-live for cached instance data to improve performance.

#### `debug` (boolean)
**Default:** `false`

Enable detailed debug logging for reconciler operations.

### Backup Configuration (`reconciler.backup`)

The reconciler includes advanced backup functionality that creates compressed, deduplicated backups of service instance data.

#### `enabled` (boolean)
**Default:** `true`

Enable backup functionality. When enabled, the reconciler will create backups of service instance data before making changes.

#### `retention_count` (integer)
**Default:** `5`

Number of backup versions to retain per service instance. Older backups beyond this count will be automatically deleted.

#### `retention_days` (integer)
**Default:** `0` (disabled)

Maximum age of backups in days. Backups older than this will be deleted. Set to `0` to disable age-based retention.

#### `compression_level` (integer)
**Default:** `9`

Gzip compression level for backup archives (1-9, where 9 is maximum compression). Higher levels provide better compression but use more CPU.

#### `cleanup_enabled` (boolean)
**Default:** `true`

Enable automatic cleanup of old backups based on retention policies.

#### `backup_on_update` (boolean)
**Default:** `true`

Create a backup before updating service instances. Recommended for data safety.

#### `backup_on_delete` (boolean)
**Default:** `true`

Create a final backup before deleting service instances. Provides recovery option for accidental deletions.

### Backup Storage Format

Backups are stored in Vault using a new deduplicated format:

- **Location:** `secret/backups/{instance-id}/{sha256}`
- **Format:** Base64-encoded, gzip-compressed JSON export compatible with `safe export/import`
- **Deduplication:** SHA256 hashing prevents storing identical backups
- **Content:** Complete export of all data under `secret/{instance-id}`

### Backup Recovery

The credential recovery system automatically attempts to restore missing data from backups:

1. **New Format Recovery:** Attempts recovery from compressed backups at new location
2. **Legacy Fallback:** Falls back to old backup format if new format not available
3. **Automatic Detection:** Identifies and restores credential data from backup archives
4. **Metadata Tracking:** Records recovery information in instance metadata

## Example Configuration

```yaml
debug: false
env: "production"
web-root: "./ui"
shareable: true

services:
  skip_tls_verify:
    - rabbitmq

vm_monitoring:
  enabled: true
  normal_interval: 3600
  failed_interval: 300
  max_retries: 3
  timeout: 30
  max_concurrent: 3

reconciler:
  enabled: true
  interval: "5m"
  max_concurrency: 5
  batch_size: 10
  retry_attempts: 3
  retry_delay: "30s"
  cache_ttl: "5m"
  debug: false
  backup:
    enabled: true
    retention_count: 5
    retention_days: 0
    compression_level: 9
    cleanup_enabled: true
    backup_on_update: true
    backup_on_delete: true

broker:
  username: "cf-broker"
  password: "secure-password"
  port: "3000"
  bind_ip: "0.0.0.0"
  read_timeout: 120
  write_timeout: 120
  idle_timeout: 300
  tls:
    enabled: true
    port: "443"
    certificate: "/etc/ssl/certs/blacksmith.pem"
    key: "/etc/ssl/private/blacksmith.key"

vault:
  address: "https://vault.example.com:8200"
  skip_ssl_validation: false
  credentials: "/etc/blacksmith/vault-creds"

bosh:
  address: "https://bosh.example.com:25555"
  username: "blacksmith"
  password: "bosh-password"
  skip_ssl_validation: false
  network: "services"
  stemcells:
    - name: "bosh-aws-xen-hvm-ubuntu-jammy-go_agent"
      version: "1.181"
      url: "https://bosh.io/d/stemcells/bosh-aws-xen-hvm-ubuntu-jammy-go_agent"
      sha1: "example-sha1-hash"

shield:
  enabled: true
  address: "https://shield.example.com"
  agent: "blacksmith-agent"
  auth_method: "token"
  token: "shield-auth-token"
  tenant: "blacksmith-tenant"
  store: "s3-backup-store"
  schedule: "daily 2am"
  retain: "30d"
  enabled_on_targets: ["redis", "postgres"]

forges:
  auto-scan: true
  scan-paths:
    - "/var/vcap/jobs"
    - "/var/vcap/data/blacksmith"
  scan-patterns:
    - "*-forge/templates"
    - "*-forge"    
```
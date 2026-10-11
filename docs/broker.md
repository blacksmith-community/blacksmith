# Blacksmith Service Broker

Blacksmith is a Cloud Foundry service broker that provisions and manages production-grade services using BOSH deployments.
It provides a  platform for deploying, monitoring, and managing services with enterprise-grade features that can be used including shield backups, monitoring, and credential management.

## Overview

Blacksmith serves as an intermediary between Cloud Foundry and BOSH, enabling:
- **Service Provisioning**: Automated deployment of services via BOSH
- **Credential Management**: Secure credential storage and dynamic user creation
- **Backup Integration**: Automated backups via Shield
- **Health Monitoring**: Continuous VM and service health monitoring
- **SSH Access**: Direct SSH access to service instances via web interface
- **Reconciliation**: Automated synchronization between CF and BOSH state

## Architecture

### Core Components

#### Broker Core (`broker.go`)
- **Broker**: Main service broker implementing Cloud Foundry Service Broker API
- **Plan Management**: Service plan validation and limit enforcement
- **Async Operations**: Background provisioning and deprovisioning

#### API Layer (`api.go`, `internal_api.go`)
- **API**: Main HTTP handler with authentication
- **InternalApi**: Extended management interface with web UI
- **WebSocket Support**: Real-time SSH session management

#### Storage and State Management
- **Vault Integration** (`vault.go`): Credential and metadata storage
- **Plan Storage** (`plan_store.go`): Plan file management and versioning
- **Index Management**: Service instance tracking and status

#### BOSH Integration (`bosh/`)
- **Pooled Director**: Connection pooling for BOSH API calls
- **Director Adapter**: BOSH operations wrapper
- **Deployment Management**: Manifest generation and deployment lifecycle

#### Reconciliation System (`pkg/reconciler/`)
- **Reconciler Manager**: Automated state synchronization
- **Scanner**: Discovery of BOSH deployments and CF service instances
- **Synchronizer**: Data consistency maintenance
- **Updater**: Safe deployment updates with backup support

#### Monitoring (`vm_monitor.go`)
- **VM Monitor**: Health checking of service instances
- **Service Monitor**: Individual service health tracking
- **Status Aggregation**: Overall health reporting

#### Service-Specific Components
- **RabbitMQ Services** (`services/rabbitmq/`): RabbitMQ-specific operations
- **SSH Services** (`bosh/ssh/`): Secure shell access to instances
- **WebSocket Handler** (`websocket/`): Real-time SSH over WebSocket

### Data Flow

```
CF Client → Broker API → Service Plans → BOSH Deployment → Vault Storage
    ↓              ↓           ↓              ↓              ↓
  CF API      Async Ops     Manifest      SSH Access     Reconciler
    ↓              ↓           ↓              ↓              ↓
  Shield      VM Monitor   Credentials      Web UI         CF Sync
```

## Configuration

### Main Configuration Structure

```yaml
# Core broker settings
broker:
  username: "admin"                    # Broker API authentication
  password: "secret"                   # Broker API password
  port: "3000"                        # HTTP port
  bind_ip: "0.0.0.0"                  # Bind address
  read_timeout: 120                   # HTTP read timeout (seconds)
  write_timeout: 120                  # HTTP write timeout (seconds)
  idle_timeout: 300                   # HTTP idle timeout (seconds)

  # TLS Configuration
  tls:
    enabled: true                     # Enable HTTPS
    port: "3443"                     # HTTPS port
    certificate: "/path/to/cert.pem" # TLS certificate file
    key: "/path/to/key.pem"          # TLS private key file
    protocols: "TLS1.2,TLS1.3"       # Supported TLS versions
    ciphers: "ECDHE-RSA-AES256-GCM-SHA384:..." # Cipher suites
    reuse_after: 3600                # Session reuse timeout

  # HTTP Compression
  compression:
    enabled: true                     # Enable gzip compression
    level: 6                         # Compression level (1-9)
    min_size: 1000                   # Minimum response size to compress
    mime_types:                      # MIME types to compress
      - "application/json"
      - "text/html"
      - "text/css"
      - "application/javascript"

  # Cloud Foundry Integration
  cf:
    broker_url: "https://blacksmith.cf.domain"  # Broker URL for CF registration
    broker_user: "admin"                        # CF service broker user
    broker_pass: "secret"                       # CF service broker password
    apis:                                       # CF API endpoints for management
      - name: "production"
        url: "https://api.cf.domain"
        username: "cf-admin"
        password: "cf-secret"
        skip_ssl_verification: false

# Vault Configuration
vault:
  address: "https://vault.domain:8200"  # Vault server URL
  token: "vault-token"                  # Vault authentication token
  skip_ssl_validation: false            # Skip SSL verification
  credentials: "/path/to/vault/creds"   # Vault credential file path
  cacert: "/path/to/ca.pem"            # CA certificate for verification

# BOSH Director Configuration
bosh:
  address: "https://bosh.domain:25555"  # BOSH director URL
  username: "admin"                     # BOSH authentication username
  password: "secret"                    # BOSH authentication password
  skip_ssl_validation: false            # Skip SSL verification
  cacert: "/path/to/ca.pem"            # CA certificate
  network: "default"                    # Default network for deployments
  max_connections: 10                   # Connection pool size
  connection_timeout: 300               # Connection timeout (seconds)

  # SSH Configuration
  ssh:
    timeout: 600                       # SSH session timeout (seconds)
    connect_timeout: 30                # SSH connection timeout (seconds)
    session_init_timeout: 60           # Session initialization timeout
    output_read_timeout: 2             # Output read timeout (seconds)
    max_concurrent: 10                 # Maximum concurrent SSH sessions
    max_output_size: 1048576          # Maximum output size (bytes)
    keep_alive: 10                    # SSH keep-alive interval (seconds)
    retry_attempts: 3                 # Connection retry attempts
    retry_delay: 5                    # Retry delay (seconds)

    # WebSocket Configuration
    websocket:
      enabled: true                   # Enable WebSocket SSH
      read_buffer_size: 4096         # WebSocket read buffer size
      write_buffer_size: 4096        # WebSocket write buffer size
      handshake_timeout: 10          # WebSocket handshake timeout (seconds)
      max_message_size: 32768        # Maximum message size (bytes)
      ping_interval: 30              # WebSocket ping interval (seconds)
      pong_timeout: 10               # WebSocket pong timeout (seconds)
      max_sessions: 50               # Maximum concurrent WebSocket sessions
      session_timeout: 1800          # WebSocket session timeout (seconds)
      enable_compression: true       # Enable WebSocket compression

  # BOSH Releases and Stemcells
  releases:
  - name: redis-forge
    version: 1.3.3
    url: https://github.com/genesis-community/redis-forge-boshrelease/releases/download/v1.3.3/redis-forge-1.3.3.tgz
    sha1: sha256:4ec3e2f921d9170bbdd3e19031bd407cab71b00e818bd9927f524f8b0dba95fe

  stemcells:
    - name: "bosh-warden-boshlite-ubuntu-noble-go_agent"
      version: "1.25"
      url: "https://bosh.io/d/stemcells/bosh-warden-boshlite-ubuntu-noble-go_agent"
      sha1: "a743eafa91a69306da0b7d420ceb3621a277e9c5"

# Shield Backup Configuration
shield:
  enabled: true                       # Enable backup integration
  address: "https://shield.domain"    # Shield server URL
  skip_ssl_validation: false          # Skip SSL verification
  agent: "default"                    # Shield agent identifier
  auth_method: "token"                # Authentication method (token|local)
  token: "shield-token"               # Shield authentication token
  username: "admin"                   # Shield username (local auth)
  password: "secret"                  # Shield password (local auth)
  tenant: "blacksmith"                # Shield tenant
  store: "default"                    # Backup store
  schedule: "daily 6am"               # Backup schedule
  retain: "7d"                        # Retention policy
  enabled_on_targets: ["rabbitmq"]    # Services to backup

# Service Configuration
services:
  skip_tls_verify: ["rabbitmq"]       # Services to skip TLS verification

# Reconciliation Configuration
reconciler:
  enabled: true                       # Enable deployment reconciliation
  interval: "5m"                      # Reconciliation interval
  max_concurrency: 5                  # Maximum concurrent reconciliations
  batch_size: 10                      # Batch size for processing
  retry_attempts: 3                   # Number of retry attempts
  retry_delay: "30s"                  # Delay between retries
  cache_ttl: "1h"                     # Cache TTL for deployment data
  debug: false                        # Enable debug logging

  # Backup Configuration
  backup:
    enabled: true                     # Enable pre-update backups
    retention_days: 7                 # Backup retention period
    storage_path: "backups/"          # Vault storage path for backups

# VM Monitoring Configuration
vm_monitoring:
  enabled: true                       # Enable VM health monitoring
  normal_interval: 3600               # Seconds between checks of a healthy deployment
  failed_interval: 300                # Seconds between checks of an unhealthy deployment
  timeout: 300                        # Deadline for each check's VM listing (seconds)
  max_concurrent: 5                   # Maximum concurrent health checks

# General Settings
debug: false                          # Enable debug logging
web-root: "/opt/blacksmith/ui"        # Web UI static files path
env: "production"                     # Environment identifier
shareable: false                      # Allow shared service instances

# Forge Configuration
forges:
  default: "/opt/blacksmith/services"  # Default service definitions path
```

## Workflow and Operations

### Service Provisioning Workflow

1. **Request Reception** (`main.go:142-710`)
   - Client sends provisioning request to `/v2/service_instances/{instance_id}`
   - Broker validates async operation support
   - Request immediately recorded in Vault with status `request_received`

2. **Plan Validation** (`broker.go:212-438`)
   - Service and plan lookup from catalog
   - Service limit enforcement
   - Status updated to `validated` in Vault index

3. **Async Provisioning** (`async.go:14-186`)
   - Background goroutine handles actual deployment
   - BOSH director UUID retrieval
   - Service initialization script execution
   - BOSH manifest generation
   - Release upload (if needed)
   - BOSH deployment creation
   - Task ID tracking for progress monitoring

4. **Progress Tracking** (`vault.go`)
   - Real-time status updates in Vault
   - Progress tracking with detailed messages
   - Task ID storage for operation monitoring

### Service Binding Workflow

1. **Credential Retrieval** (`broker.go:918-1006`)
   - Static credentials fetched from deployment
   - Service-specific credential processing

2. **Dynamic User Creation** (RabbitMQ)
   - Unique binding user created via RabbitMQ Management API
   - Permissions granted for specific vhost
   - Credentials replaced with dynamic values

3. **Credential Response**
   - Admin credentials filtered out
   - Service-ready credentials returned to CF

### Reconciliation Process (`pkg/reconciler/`)

1. **Discovery Phase**
   - Scan BOSH deployments for Blacksmith services
   - Query CF for service instance metadata
   - Build  instance dataset

2. **Synchronization Phase**
   - Compare BOSH and CF service states
   - Update Vault index with current information
   - Handle orphaned instances and deployments

3. **Update Phase**
   - Safe deployment updates with backup support
   - Rolling updates with health verification
   - Automatic rollback on failure

### Monitoring Operations (`vm_monitor.go`)

1. **Health Checking**
   - Periodic VM status verification
   - Service-specific health endpoints
   - Overall deployment health calculation

2. **Status Storage**
   - Health status stored in Vault
   - Historical health data tracking
   - Alert generation for failures

## API Endpoints

### Cloud Foundry Service Broker API
- `GET /v2/catalog` - Service catalog
- `PUT /v2/service_instances/{instance_id}` - Provision service
- `PATCH /v2/service_instances/{instance_id}` - Update service
- `DELETE /v2/service_instances/{instance_id}` - Deprovision service
- `GET /v2/service_instances/{instance_id}/last_operation` - Operation status
- `PUT /v2/service_instances/{instance_id}/service_bindings/{binding_id}` - Create binding
- `DELETE /v2/service_instances/{instance_id}/service_bindings/{binding_id}` - Delete binding

### Internal Management API
- `GET /b/status` - Broker health and status
- `GET /b/instance` - Service instance listing
- `GET /b/instance/{instance_id}` - Instance details
- `GET /b/instance/{instance_id}/manifest` - Deployment manifest
- `GET /b/instance/{instance_id}/credentials` - Service credentials
- `GET /b/instance/{instance_id}/backup` - Backup operations
- `POST /b/instance/{instance_id}/ssh` - SSH session creation
- `GET /b/vm-status` - VM health monitoring
- `POST /b/vm-refresh` - Trigger health check

### CF Registration API
- `GET /api/cf/registrations` - List CF registrations
- `POST /api/cf/registrations` - Create CF registration
- `GET /api/cf/registrations/{id}` - Get registration details
- `PUT /api/cf/registrations/{id}` - Update registration
- `DELETE /api/cf/registrations/{id}` - Delete registration
- `POST /api/cf/test-connection` - Test CF API connection

### WebSocket APIs
- `GET /b/instance/{instance_id}/ssh/ws` - SSH WebSocket endpoint
- `GET /b/instance/{instance_id}/rabbitmq/ws` - RabbitMQ command WebSocket
- `GET /b/instance/{instance_id}/rabbitmq/plugins/ws` - RabbitMQ plugins WebSocket

## Service Management Features

### Dynamic Credential Management

**RabbitMQ Integration**:
- Creates unique users per binding using binding ID as username
- Generates secure random passwords for each binding
- Grants appropriate permissions for the service vhost
- Automatic cleanup on unbinding

**Credential Lifecycle**:
1. Static admin credentials retrieved from deployment
2. Dynamic user created via RabbitMQ Management API
3. Permissions configured for service access
4. Credentials returned with dynamic values
5. User deleted during unbinding process

### Backup Integration (Shield)

**Configuration Options**:
- Backup scheduling (daily, weekly, custom)
- Retention policies (7d, 30d, etc.)
- Target-specific backup enablement
- Multiple authentication methods

**Backup Workflow**:
1. Shield agent discovery and registration
2. Backup job configuration
3. Scheduled backup execution
4. Retention policy enforcement

### SSH Access Management

**Security Features**:
- Host keys checked against the keys the BOSH director reports
- Configurable timeouts and limits
- Connection pooling and rate limiting
- WebSocket-based real-time access

**Access Methods**:
- Direct SSH via web interface
- Command execution with streaming output
- File transfer capabilities
- Multi-session support

**SSH User Cleanup**:

Every SSH session and command runs as a throwaway user that the BOSH director creates on the VM and removes again when the session ends. Before the broker asks the director to create a user, it writes a record of that user to Vault at `secret/blacksmith/ssh-cleanups/<user>`. If the record cannot be written, the broker refuses the session and creates nothing. The record is cleared once the director confirms the user is gone, which happens when the cleanup task ends done, when the deployment no longer exists, or when the instance has no VM. A setup that the director refuses outright, for example because the deployment does not exist or the broker's client lacks permission, creates no user, so its record is cleared at once.

A setup can fail after the broker sent it, for example when its task fails, when the caller gives up, or when the director's answer is lost. The director runs a setup it has started to its end even after a cancel, so the broker waits for the setup to end and only then sends the cleanup. When the broker knows the setup task, it waits up to a minute for that task to end. When the director's answer was lost, the broker doesn't know the task, so it first waits 10 seconds for the request to become a task, and then waits up to a minute until no SSH setup is queued or running on the deployment. The caller gets its error at once, and the cleanup follows in the background. If the setup has not ended within that time, the broker sends no cleanup and leaves the user to the sweep below.

The broker waits at most three minutes for a setup task to finish, even when the caller would wait longer, as an asynchronous SSH command does. When that bound passes, the broker asks the director to cancel the task, and the cleanup follows as it does for any failed setup. The bound covers only the setup, so an open session or a running command is never cut off by it.

A record that is still in Vault means the user may still be on the VM, because a restart, a lost answer, or a failed cleanup task got in the way. The broker sweeps these records once at boot and every 10 minutes after that, and sends a fresh cleanup for each one. The sweep skips a user whose session this broker still has open, and it skips every user on a deployment where an SSH setup is queued or running, so a cleanup never overtakes a setup. It asks the director about running setups again right before each cleanup it sends, because a setup can start while the sweep is busy with an earlier record. When a retried cleanup fails, the broker keeps the record, counts the attempt and stores the error in it, and logs a warning that names the user, the instance, the deployment, and the attempt.

The sweep treats every record under that path as its own. Each broker must therefore have its own Vault mount. Two brokers that share a mount would remove each other's SSH users while their sessions were still open.

A record that the broker reads but cannot use, because it does not decode, names a user other than the one in its path, or lacks a deployment or an instance to clean up, is left out of the sweep, and the sweep's error names its path. Remove such a record by hand once you have confirmed that its user is gone from the VM. When Vault cannot list the records or read one of them, the whole pass stops and sends no cleanups, and the next pass tries again.

### VM Health Monitoring

**Monitoring Capabilities**:
- Periodic health checks
- Service-specific health endpoints
- VM status aggregation
- Historical health data

**Alerting and Response**:
- Health status changes logged
- Failed deployment detection
- Automatic retry mechanisms
- Manual refresh triggers

## Configuration Examples

### Basic Configuration
```yaml
broker:
  username: "blacksmith"
  password: "secure-password"
  port: "3000"

vault:
  address: "https://vault.local:8200"
  token: "hvs.XXXXXX"

bosh:
  address: "https://bosh.local:25555"
  username: "admin"
  password: "admin-password"
  network: "blacksmith"
```

### Production Configuration
```yaml
broker:
  username: "blacksmith"
  password: "complex-secure-password"
  port: "3000"
  read_timeout: 300
  write_timeout: 300
  idle_timeout: 600
  tls:
    enabled: true
    port: "3443"
    certificate: "/etc/ssl/blacksmith/cert.pem"
    key: "/etc/ssl/blacksmith/key.pem"
    protocols: "TLS1.2,TLS1.3"
  compression:
    enabled: true
    level: 6
    min_size: 1000
  cf:
    broker_url: "https://blacksmith.apps.domain.com"
    apis:
      - name: "production"
        url: "https://api.cf.domain.com"
        username: "blacksmith-cf-user"
        password: "cf-integration-password"

vault:
  address: "https://vault.domain.com:8200"
  credentials: "/etc/blacksmith/vault-creds"
  cacert: "/etc/ssl/ca-certificates.pem"

bosh:
  address: "https://bosh.domain.com:25555"
  username: "blacksmith"
  password: "bosh-password"
  cacert: "/etc/ssl/bosh-ca.pem"
  network: "services"
  max_connections: 20
  connection_timeout: 600
  ssh:
    timeout: 1800
    max_concurrent: 25
    websocket:
      enabled: true
      max_sessions: 100

shield:
  enabled: true
  address: "https://shield.domain.com"
  auth_method: "token"
  token: "shield-api-token"
  tenant: "blacksmith-production"
  store: "s3-primary"
  schedule: "daily 2am"
  retain: "30d"
  enabled_on_targets: ["rabbitmq", "redis", "postgresql"]

reconciler:
  enabled: true
  interval: "10m"
  max_concurrency: 10
  batch_size: 20
  backup:
    enabled: true
    retention_days: 14

vm_monitoring:
  enabled: true
  normal_interval: 300
  failed_interval: 120
  timeout: 300
  max_concurrent: 15

services:
  skip_tls_verify: []

debug: false
env: "production"
web-root: "/opt/blacksmith/ui"
```

## Operations and Maintenance

### Deployment Lifecycle

1. **Pre-deployment**: Service plan validation, limit checking
2. **Deployment**: BOSH manifest generation, release upload, deployment creation
3. **Post-deployment**: Health verification, credential setup, backup configuration
4. **Updates**: Safe rolling updates with backup and rollback support
5. **Decommission**: Resource cleanup, backup retention, audit trail

### Monitoring and Alerting

**Health Indicators**:
- Broker API availability
- BOSH director connectivity
- Vault accessibility
- Service instance health
- SSH service availability

**Operational Metrics**:
- Service provisioning success rate
- Average provisioning time
- Resource utilization
- Backup success rate
- SSH session counts

### Troubleshooting

**Common Issues**:
- BOSH connection failures: Check network connectivity and credentials
- Vault authentication errors: Verify token validity and permissions
- Service limit exceeded: Review service quotas and usage
- SSH connection failures: Check network access from the broker to the VM. A host key mismatch means the VM was recreated since the SSH setup, or something is intercepting the connection
- Backup failures: Check Shield connectivity and storage availability

**Debug Tools**:
- Debug logging (`debug: true`)
- Instance status tracking via Vault
- BOSH task monitoring
- WebSocket connection debugging
- Reconciler status reporting

### Security Considerations

**Authentication and Authorization**:
- HTTP Basic Auth for broker API
- Vault token-based authentication
- BOSH UAA integration
- CF service broker registration security

**Network Security**:
- TLS encryption for all communications
- Certificate-based authentication
- SSH host key verification
- Configurable cipher suites

**Credential Management**:
- Vault-based secure storage
- Dynamic credential generation
- Automatic credential rotation support
- Admin credential isolation

## Performance and Scaling

### Connection Management
- BOSH director connection pooling (configurable pool size)
- HTTP timeout configuration
- SSH session limits and timeouts
- WebSocket session management

### Resource Optimization
- HTTP response compression
- Async operation handling
- Batch processing for reconciliation
- Configurable concurrency limits

### High Availability
- External state storage (Vault), including the records of SSH users still to be removed
- One Vault mount per broker, because the SSH cleanup sweep treats every record on its mount as its own
- Graceful shutdown handling
- Health check endpoints for load balancer integration

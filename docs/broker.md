# Blacksmith Service Broker

Blacksmith is a Cloud Foundry service broker that provisions and manages production-grade services using BOSH deployments. It provides a platform for deploying, monitoring, and managing services, with features that include SHIELD backups, monitoring, and credential management.

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

#### Broker Core (`internal/broker`)
- **Broker**: Main service broker implementing Cloud Foundry Service Broker API
- **Plan Management**: Service plan validation and limit enforcement
- **Async Operations**: Background provisioning and deprovisioning
- **CredHub Cleanup**: Removal of a deprovisioned instance's director credentials

#### API Layer (`internal/api`, `internal/handlers`)
- **Router**: Registers every `/b/` route and sends the rest to the service handlers
- **Handlers**: One package per area, such as instances, deployments, tasks, certificates, upgrade, and CF registration
- **WebSocket Support**: Real-time SSH and RabbitMQ sessions

#### Storage and State Management
- **Vault Integration** (`internal/vault`): Credential and metadata storage
- **Plan Storage** (`internal/planstore`): Plan file management and versioning
- **Index Management**: Service instance tracking and status
- **Audit Records** (`internal/audit`): Changes to settings, endpoints, and instances

#### BOSH Integration (`internal/bosh`)
- **Pooled Director**: Connection pooling for BOSH API calls
- **Director Adapter**: BOSH operations wrapper
- **SSH** (`internal/bosh/ssh`): Throwaway users and commands on service VMs
- **Deployment Management**: Manifest generation and deployment lifecycle

#### Reconciliation System (`pkg/reconciler`, `internal/recovery`)
- **Reconciler Manager**: Automated state synchronization
- **Scanner**: Discovery of BOSH deployments and CF service instances
- **Synchronizer**: Data consistency maintenance and orphan marking
- **Updater**: Safe deployment updates with backup support

#### Monitoring (`internal/vmmonitor`)
- **VM Monitor**: Health checking of service instances
- **Service Monitor**: Individual service health tracking
- **Status Aggregation**: Overall health reporting

#### Batch Operations (`internal/upgrade`)
- **Upgrade, Certificate, and Command Jobs**: Tasks that work through many instances
- **Certificate Inventory** (`internal/certinventory`): Scans instances for certificates

#### Service-Specific Components
- **RabbitMQ and Redis Handlers** (`internal/handlers/services`): Service-specific operations
- **WebSocket Handlers** (`internal/handlers/ssh/websocket`, `internal/handlers/services/rabbitmq/websocket`): Real-time sessions over WebSocket

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

  # HTTP Compression
  compression:
    enabled: true                     # Enable gzip compression
    level: 6                         # Compression level (1-9)
    min_size: 1000                   # Minimum response size to compress
    content_types:                   # MIME types to compress
      - "application/json"
      - "text/html"
      - "text/css"
      - "application/javascript"

  # Cloud Foundry Integration
  cf:
    enabled: true                               # Allow CF registration
    broker_url: "https://blacksmith.cf.domain"  # Broker URL for CF registration
    broker_user: "admin"                        # CF service broker user
    broker_pass: "secret"                       # CF service broker password
    apis:                                       # CF API endpoints for management, keyed by name
      production:
        name: "production"
        endpoint: "https://api.cf.domain"
        username: "cf-admin"
        password: "cf-secret"
        skip_ssl_validation: false

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
  cacert: |                             # PEM text of the director's CA certificate
    -----BEGIN CERTIFICATE-----
    MIIB...
    -----END CERTIFICATE-----
  network: "default"                    # Default network for deployments
  max_connections: 10                   # Connection pool size
  connection_timeout: 300               # Connection timeout (seconds)

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

# SSH Configuration (this was bosh.ssh in older versions)
ssh:
  timeout: 600                         # SSH session timeout (seconds)
  connect_timeout: 30                  # SSH connection timeout (seconds)
  session_init_timeout: 60             # Session initialization timeout
  output_read_timeout: 2               # Output read timeout (seconds)
  max_concurrent: 10                   # Maximum concurrent SSH sessions
  max_output_size: 1048576             # Maximum output size (bytes)
  keep_alive: 10                       # SSH keep-alive interval (seconds)
  retry_attempts: 3                    # Connection retry attempts
  retry_delay: 5                       # Retry delay (seconds)

  # WebSocket Configuration
  websocket:
    enabled: true                      # Enable WebSocket SSH
    read_buffer_size: 4096             # WebSocket read buffer size
    write_buffer_size: 4096            # WebSocket write buffer size
    handshake_timeout: 10              # WebSocket handshake timeout (seconds)
    max_message_size: 32768            # Maximum message size (bytes)
    ping_interval: 5                   # WebSocket ping interval (seconds)
    pong_timeout: 10                   # WebSocket pong timeout (seconds)
    max_sessions: 50                   # Maximum concurrent WebSocket sessions
    session_timeout: 1800              # Idle limit and maximum lifetime of a session (seconds)
    enable_compression: false          # Enable WebSocket compression

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
  max_concurrency: 4                  # Maximum concurrent reconciliations
  batch_size: 10                      # Batch size for processing
  retry_attempts: 3                   # Number of retry attempts
  debug: false                        # Enable debug logging

  # Backup Configuration
  backup:
    enabled: true                     # Enable pre-update backups
    retention_days: 7                 # Backup retention period

# VM Monitoring Configuration
vm_monitoring:
  enabled: true                       # Enable VM health monitoring
  normal_interval: 3600               # Seconds between checks of a healthy deployment
  failed_interval: 300                # Seconds between checks of an unhealthy deployment
  timeout: 300                        # Deadline for each check's VM listing (seconds)
  max_concurrent: 3                   # VM monitor workers

# General Settings
debug: false                          # Enable debug logging
web-root: "/opt/blacksmith/ui"        # Web UI static files path
env: "production"                     # Environment identifier
shareable: false                      # Allow shared service instances

# Forge Configuration
forges:
  auto-scan: true                     # Look for forges on the broker VM
  scan-paths:
    - "/var/vcap/jobs"
  scan-patterns:
    - "*-forge"
```

[config.md](config.md) is the full reference for every key. The sample leaves out `credhub`, `certificates`, and `commands`, which are optional and have their own sections there.

## Workflow and Operations

### Service Provisioning Workflow

1. **Request Reception**
   - Client sends provisioning request to `/v2/service_instances/{instance_id}`
   - Broker validates async operation support
   - Request immediately recorded in Vault with status `request_received`

2. **Plan Validation**
   - Service and plan lookup from catalog
   - Parameters checked against the plan's schema, so a bad parameter gets a synchronous 400 `InvalidParameters` before anything is written
   - Service limit enforcement
   - Status updated to `validated` in Vault index

3. **Async Provisioning**
   - Background goroutine handles actual deployment
   - BOSH director UUID retrieval
   - Service initialization script execution
   - BOSH manifest generation
   - Release upload (if needed)
   - BOSH deployment creation
   - Task ID tracking for progress monitoring

4. **Progress Tracking**
   - Real-time status updates in Vault
   - Progress tracking with detailed messages
   - Task ID storage for operation monitoring

### Service Update Workflow

A `PATCH /v2/service_instances/{instance_id}` changes an instance's parameters, or the platform context it carries. A plan change and a `maintenance_info` change are refused with 422 for now, and so is an update to an instance whose provision did not succeed.

The broker merges the request over the stored parameters. A key in the request sets or replaces the stored value, a key whose value is `null` removes it, and a key the request leaves out keeps its value. The merged parameters are checked against the plan's schema, and a bad one gets a 400 `InvalidParameters`. The broker then renders the manifest for the current parameters and for the merged ones, and compares the two. When only the merged parameters fail to render, the update gets a 400 `InvalidParameters`. When the current ones fail too, the plan's template is at fault, so the update gets a 500 that tells the platform an operator must fix the plan.

- **No effect**

  When the two renders match, nothing is deployed. The broker records the merged parameters and any new platform context, such as a renamed space, and answers 200.

- **A change to deploy**

  When the renders differ, the platform must accept an asynchronous answer, or the broker refuses the update with 422 `AsyncRequired`. The broker answers 202 with the operation `update`, and a background goroutine takes the change from there. It reads the live manifest from the director, carries forward any newer release and stemcell versions an upgrade applied, saves the live manifest for a possible rollback, uploads the releases the manifest still needs, and deploys it with a fresh context ID. The goroutine holds the instance's claim until the update settles, so an upgrade, a certificate rotation, or a manifest change finds the instance busy. The steps before the deploy have a two-hour limit, and once the deploy is sent, the broker follows the BOSH task for as long as it runs, even while the director can't be reached, and only a shutdown stops it early. A repeat of the same update gets 202 with the same operation, and a different update gets 422 `ConcurrencyError`.

- **Success**

  Once the deploy task succeeds, the broker commits the new parameters, the deployed manifest, and the context, and the poll answers `succeeded`. It also refreshes the instance's stored credentials, and when that refresh takes more than five minutes, the broker keeps the stored ones and settles the update anyway.

- **Failure**

  When the deploy task fails, the stored plan, parameters, and manifest keep their last good values, and the broker never deletes the deployment. With `update_rollback` off, which is the default, the poll answers `failed` with `instance_usable: true` and `update_repeatable: true`. Once the instance's plan has left the catalog, every update is refused until an operator restores the plan, so the poll answers `update_repeatable: false` until then. When a restart interrupts an update whose plan has since left the catalog, the broker follows the task to its end and settles the update as failed with a description that tells the tenant an operator must restore the plan.

- **Rollback**

  With `update_rollback` on, the broker redeploys the saved live manifest once, exactly as it was, under the same operation and the same claim. While it runs, the poll answers `in progress` and says the previous manifest is being redeployed. When the rollback restores the deployment, the poll answers `failed` with `instance_usable: true`, because the update the tenant asked for did not happen. When the rollback fails too, the poll answers `failed` with `instance_usable: false` and a description that names both BOSH tasks, and an operator should check the deployment. Nothing retries a rollback.

The broker reads the setting at the moment the deploy fails, so a change an operator saves applies to any update that fails afterward. When the director's answer to a deploy is lost, the broker looks the task up by the deploy's context ID and never sends the deploy again. A restart in the middle of an update or its rollback is picked up at the next start, which follows the recorded task or looks it up by context ID. Every update that reaches its checks leaves a change record that names the parameter keys it changed, never their values, along with the BOSH tasks and the rollback setting in effect.

### Service Binding Workflow

1. **Credential Retrieval**
   - Static credentials fetched from deployment
   - Service-specific credential processing

2. **Dynamic User Creation** (RabbitMQ)
   - Unique binding user created via RabbitMQ Management API
   - Permissions granted for specific vhost
   - Credentials replaced with dynamic values

3. **Credential Response**
   - Admin credentials filtered out
   - Service-ready credentials returned to CF

### Reconciliation Process

1. **Discovery Phase**
   - Scan BOSH deployments for Blacksmith services
   - Query CF for service instance metadata
   - Build  instance dataset

2. **Synchronization Phase**
   - Compare BOSH and CF service states
   - Update Vault index with current information
   - Mark orphaned instances, and remove them after a grace period
   - Report unclaimed deployments

3. **Update Phase**
   - Safe deployment updates with backup support
   - Rolling updates with health verification

### Monitoring Operations

1. **Health Checking**
   - Periodic VM status verification
   - Service-specific health endpoints
   - Overall deployment health calculation

2. **Status Storage**
   - Health status stored in Vault
   - Historical health data tracking
   - Deployments the director no longer has are marked `deleted`

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

Every route here takes the broker's basic authentication. [service-instance-management-workflow.md](service-instance-management-workflow.md) describes how the status, orphan, and task routes fit the instance lifecycle.

#### Broker Status and Settings

- `GET /b/status` - Broker status. Reports the build `version` and `commit`, and whether the BOSH director and Vault answer (`bosh` is `connected` or `unreachable`, and `vault` is `connected`, `sealed`, or `unreachable`, with `healthy` true only when both are connected). It also carries `environment`, `env`, `timestamp`, the `instances` index, and the `plans`. The health result is cached for five seconds, and a degraded broker still answers 200.
- `GET /b/internal/health` - Health check for load balancers
- `GET /b/instance` - Details of the Blacksmith VM the broker runs on
- `GET /b/config/ssh/ui-terminal-status` - Whether the SSH terminal UI is on
- `GET /b/blacksmith/config` - The broker's running configuration
- `GET /b/blacksmith/{logs,vms,events,manifest,credentials}` - The Blacksmith deployment's own logs, VMs, events, manifest, and credentials
- `GET /b/blacksmith/settings` and `POST /b/blacksmith/settings` - Read and change `vault_max_versions` and `reconciler_interval`
- `GET /b/blacksmith/changes` - Audited changes, with `scope` set to `settings`, `endpoints`, or `instances`, and `limit` from 1 to 100
- `GET /b/orphans` - Entries marked orphaned and unclaimed BOSH deployments
- `POST /b/cleanup` - Reserved for a manual cleanup job, and it answers 501 Not Implemented. The reconciler removes orphans on its own.

#### Service Instances

Each route takes the instance ID in place of `{id}`.

- `GET /b/{id}/vms`, `/logs`, `/events`, `/manifest`, `/credentials`, `/details`, and `/config` - What the instance has on BOSH and in Vault.
- `GET /b/{id}/task/log` and `/task/debug` - The log of the instance's latest BOSH task
- `POST /b/{id}/restart` and `POST /b/{id}/recreate` - Restart or recreate every VM of the instance
- `PUT /b/{id}/resurrection` and `DELETE /b/{id}/resurrection` - Toggle BOSH resurrection for the instance, and delete its resurrection config
- `POST /b/{id}/ssh/execute` - Run one command over SSH
- `POST /b/{id}/rabbitmq/ssh/{command}` - Run a RabbitMQ command over SSH
- `GET`, `POST`, and `DELETE /b/{id}/certificates/trusted`, and `POST /b/{id}/certificates/trusted/file` - Read the instance's trusted certificates
- `/b/{id}/rabbitmq/{test,publish,consume,queues,queue-ops,management}` and `/b/{id}/redis/{test,info,set,get,delete,command,keys,flush}` - Test and inspect a RabbitMQ or Redis instance

#### BOSH

- `GET /b/bosh/pool-stats` - Connection pool usage
- `GET /b/bosh/stemcells` and `GET /b/bosh/releases` - What the director holds
- `GET /b/bosh/deployment-releases` - Releases in use by each deployment
- `GET /b/bosh/vm-monitor` - The VM monitor's summary
- `GET /b/tasks` - BOSH tasks. It takes `state`, `limit` (default 50), and `offset` for paging, and `counts=1` adds the number of tasks in each state.
- `GET /b/tasks/{id}`, `GET /b/tasks/{id}/output`, and `POST /b/tasks/{id}/cancel` - One task, its output, and its cancel
- `GET /b/configs`, `GET /b/configs/{id}`, `GET /b/configs/{type}/{name}/versions`, and `GET /b/configs/{type}/{name}/diff` - The director's configs
- `GET /b/service-filter-options` - The values the console offers in its service filters

#### Deployments

- `GET /b/deployments/{name}` and `DELETE /b/deployments/{name}` - Read or delete a deployment
- `GET /b/deployments/{name}/{manifest,manifest-details,vms,instances,events}` - Read a deployment's manifest, VMs, instances, or events
- `POST /b/deployments/{name}/manifest` - Update the manifest
- `POST /b/deployments/{name}/{restart,recreate,stop,start}` - Change what the deployment's VMs are doing
- `GET /b/deployments/{name}/errands` and `POST /b/deployments/{name}/errands/{errand}` - List or run errands

#### Certificates

- `GET /b/certificates/inventory`, `POST /b/certificates/scan`, and `POST /b/certificates/rotation/preview` - The inventory, a fresh scan, and a rotation preview
- `GET /b/certificates/instances/{id}` and `GET /b/certificates/items/{id}` - One instance's certificates, or one certificate
- `GET /b/certificates/settings` and `PUT /b/certificates/settings` - Read and change the certificate settings
- `/b/internal/certificates/{trusted,trusted/file,blacksmith,endpoint,parse,services/...}` - The certificates the broker itself uses

### Batch Operations API

Batch Operations creates every job through `POST /b/upgrade/tasks`, and the body's `kind` picks the job. A body with no `kind` creates an upgrade, `certificates` creates a certificate job, and `command` creates a Run command job, which [Run Command](run-command.md#http-api) describes.

| Kind | Body |
|------|------|
| Upgrade | Takes `instance_ids`, `target_stemcell`, `target_releases`, an optional `upgrade_preview_id`, and an optional boolean `pause_on_failure`. |
| `certificates` | Takes `preview_id`, `client_request_id`, `name`, `reason`, and an optional boolean `pause_on_failure`. |
| `command` | Takes the fields in [Run Command](run-command.md#http-api). It refuses `pause_on_failure` with 400 `invalid_request`, because a command job always pauses after a failure. |

The value of `pause_on_failure` applies to the job it creates and never changes a broker-wide setting. An upgrade or certificate create that sends a value other than `true` or `false` answers 400 `invalid_request`, and one that sends `null` is treated as leaving it out. A command create refuses the key whatever its value, including `null`. An upgrade that leaves it out doesn't pause, as upgrades never did before the option existed. A certificate job that leaves it out takes the `pause_on_failure` of the certificate settings (`GET /b/certificates/settings`) as it stands when the job is created, so a later change to the settings leaves the job as it was. A certificate create that repeats a `client_request_id` with a different `pause_on_failure` is a different request and answers 409 `request_id_conflict`.

The task that the create returns, and every later read of it, carries `pause_on_failure` as the job runs with it. A job stored by a broker from before the option existed has no `pause_on_failure`, and it behaves as it always did. Such an upgrade never pauses, and such a certificate job follows the certificate settings as they stand when its instance fails.

#### Pausing After a Failure

When an instance fails in a job that pauses on failure, and another instance is still pending, the job pauses before it starts anything more. A deploy that is already in flight, such as one the broker picked up again after a restart, runs to its own end and is recorded. The paused task carries a `pause_reason` that names the failed instance, and the task detail shows it. An upgrade whose deploy outcome the broker can't learn pauses the same way, and its reason says that the outcome is unknown. Upgrades and certificate jobs both save the failure and the pause together, so a broker that restarts finds the job paused rather than starting its next instance. The exception is a certificate job stored by a broker from before the option existed, which has no `pause_on_failure`. Such a job can still save the two one after the other, so if the broker stops in the moment between its failure and its pause, the next instance can start after the restart. When a restart finds such a job's in-flight instance reserved by another job, the broker fails that instance and goes on to the next one without pausing. The "Continue remaining instances" control resumes the job with the instances that haven't run, and it never retries the failed one. Pause, Resume, and Cancel otherwise work as they do for any job, and a resume or a cancel clears `pause_reason`. A job whose last instance fails has nothing left to protect, so it finishes rather than pausing.

### CF Registration API
- `GET /b/cf/registrations` - List CF registrations
- `POST /b/cf/registrations` - Create CF registration
- `GET /b/cf/registrations/{id}` - Get registration details
- `PUT /b/cf/registrations/{id}` - Update registration
- `DELETE /b/cf/registrations/{id}` - Delete registration
- `POST /b/cf/registrations/{id}/register` - Register the broker with the saved CF
- `POST /b/cf/endpoints/{name}/connect` - Connect to a configured CF endpoint
- `POST /b/cf/test-connection` - Test a CF API URL, username, and password before saving them

Every `POST` under `/b/cf` needs the header `Content-Type: application/json`, even when it sends no fields, so send `{}` as the body then. Any other content type gets a 415 answer with the code `unsupported_media_type`, before the route does anything. A page on another site can't send that content type without a CORS preflight, and the broker answers none, so this keeps these routes to the broker's own UI and to clients that call it directly.

The broker doesn't store a registration's CF admin password. Creating or editing a registration drops any `password` field before the record goes to Vault at `secret/blacksmith/registrations/{id}`, and `POST /b/cf/registrations/{id}/register` takes the password in its own JSON body as `{"password": "..."}`. A record saved by an older broker may still hold a password. Register uses it only when the request brings none, and the broker never changes or migrates it. An edit keeps it as it is while the record still names the same `api_url` and `username`. An edit that changes either one drops the stored password, so register then needs the password in its body. A register request with no password, on a record that holds none, gets a 400 answer with the code `invalid_request`. No answer ever gives a password back. The create and update answers, `GET /b/cf/registrations`, and `GET /b/cf/registrations/{id}` leave the field out entirely, under any spelling of the key.

Registering the broker follows the same connection rules as the test below. The registration's API URL must start with `https://`, and the validating step fails with a readable message when it doesn't, because the API root's answer decides where the password goes.

`POST /b/cf/test-connection` takes the registration form's `api_url`, `username`, and `password` as JSON. A missing field, or an API URL that is not an `http://` or `https://` URL with a host, gets a 400 answer with the code `invalid_request`.

The test runs four checks in order and stops at the first one that fails. The `reachable` check asks the API root for an answer, and the `tls` check needs the API's certificate to verify against the system roots, as registration does. Registration has no way to skip certificate verification, so the test has none either. The `cloud_foundry` check needs the root to be a Cloud Foundry v3 root document that names an authentication endpoint. The `token` check sends the password grant that registration sends to that endpoint's `/oauth/token`, and passes when a token comes back. The test throws the token away at once.

The token request goes to the host that the API root names as its authentication endpoint, which is usually a different host from the one you typed, such as `uaa.` beside `api.`. The broker trusts the root's answer for that host and applies no domain rule to it, so the API URL you type decides where the password goes. That's why the root must answer over verified HTTPS before the password is sent anywhere.

The whole test has a 10-second limit, and it reads at most 64 KiB of any response. It follows up to 3 redirects from the API root, each only to the same scheme and host, and it follows no redirect from the token endpoint. It sends the credentials only over HTTPS.

A test that ran answers 200, whether or not a check failed. The answer lists every check with its `name`, `passed`, and `message`, and marks the checks after a failure `skipped`. A failed test also carries `"success": false`, the failed check's message in `error`, and one of the codes `cf_unreachable`, `cf_tls_failed`, `cf_not_cloud_foundry`, or `cf_auth_failed` in `code`. Messages name the step and any HTTP status, and the broker never logs the credentials or the token and never returns them.

```json
{
  "success": false,
  "error": "The API's certificate did not verify: the certificate is not signed by a trusted authority.",
  "code": "cf_tls_failed",
  "checks": [
    {"name": "reachable", "passed": true, "message": "The API answered."},
    {"name": "tls", "passed": false, "message": "The API's certificate did not verify: the certificate is not signed by a trusted authority."},
    {"name": "cloud_foundry", "passed": false, "skipped": true, "message": "Not run, because an earlier check failed."},
    {"name": "token", "passed": false, "skipped": true, "message": "Not run, because an earlier check failed."}
  ]
}
```

### Task Routes for Batch Operations

All of these sit under `/b/upgrade`.

- `POST /b/upgrade/preview` - Preview an upgrade
- `POST /b/upgrade/commands/preview`, `POST /b/upgrade/commands/discard`, and `GET /b/upgrade/commands/availability` - Preview a command run, discard its preview, and read the command switches
- `GET /b/upgrade/settings` and `PUT /b/upgrade/settings` - Read and change the batch settings
- `POST /b/upgrade/tasks` and `GET /b/upgrade/tasks` - Create and list tasks
- `GET /b/upgrade/tasks/{id}` - Read one task
- `POST /b/upgrade/tasks/{id}/pause`, `/resume`, and `/cancel` - Steer a task
- `GET /b/upgrade/tasks/{id}/output` - Read what a command run printed on one node
- `POST /b/upgrade/tasks/{id}/instances/{instance-id}/{action}` - Decide for one instance, where the action is `reconcile`, `approve`, `preview`, `resolve`, or `candidates`

### WebSocket APIs
- `GET /b/{id}/ssh/stream` - SSH terminal WebSocket for a service instance. It takes an `index` query parameter that picks the VM.
- `GET /b/blacksmith/ssh/stream` - SSH terminal WebSocket for the Blacksmith VM
- `GET /b/ssh/status` - Whether the SSH WebSocket service is available
- `GET /b/{id}/rabbitmq/rabbitmqctl/*` - RabbitMQ command WebSocket
- `GET /b/{id}/rabbitmq/plugins/*` - RabbitMQ plugins WebSocket

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
  cacert: |
    -----BEGIN CERTIFICATE-----
    MIIB...
    -----END CERTIFICATE-----
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
- Reconciler log lines, and `GET /b/orphans`

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
- `GET /b/internal/health` for load balancer integration

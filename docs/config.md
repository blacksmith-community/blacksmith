# Blacksmith Configuration Reference

This document provides documentation for all Blacksmith configuration file options. The configuration file is in YAML format and defines how Blacksmith connects to external services and operates.

## Configuration File Structure

The main configuration file contains the following top-level sections:

```yaml
broker:       # HTTP server, authentication, compression, and Cloud Foundry integration (broker.cf)
vault:        # Vault integration configuration
shield:       # SHIELD backup integration (optional)
bosh:         # BOSH director configuration
ssh:          # SSH connection and WebSocket settings
credhub:      # Director CredHub cleanup (optional)
certificates: # Certificate inventory, rotation, and in-place patching (optional)
commands:     # Run command batch operation (optional)
services:     # Service-specific behavior configuration
vm_monitoring: # VM monitoring and status tracking (optional)
reconciler:   # Deployment reconciler configuration (optional)
debug:        # Enable debug logging
web-root:     # Static web content directory
env:          # Environment identifier
shareable:    # Enable shareable service instances
forges:       # Service template discovery configuration
```

The Cloud Foundry settings are not a top-level section. They sit under `broker.cf`, as the [Cloud Foundry section](#cloud-foundry-configuration-brokercf) shows.

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
  enabled: true         # Enable VM monitoring
  normal_interval: 3600 # Check healthy deployments every hour
  failed_interval: 300  # Check unhealthy deployments every 5 minutes
  timeout: 300          # Deadline for each check's VM listing (seconds)
  max_concurrent: 3     # Most VM checks that run at once
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
**Deprecated.** The monitor no longer retries a failed BOSH call inside one check. It checks the instance again after `failed_interval` instead. A configuration that sets `max_retries` still loads, and the value has no effect. The broker logs a warning that names the setting whenever the key is present, even when its value is 0. Remove it from the file.

#### `timeout` (integer)
**Default:** `300` (5 minutes)

The deadline, in seconds, that each check gives its VM listing. The director lists a deployment's VMs through a BOSH task, and that task can wait behind other work in a busy director's queue. When the deadline passes, Blacksmith asks the director to cancel the task, records the check as failed, and checks the instance again after `failed_interval`. The deadline covers only the listing, so the check's result is still stored. A value of 0 means the default, and a negative value stops the broker from loading its configuration.

#### `max_concurrent` (integer)
**Default:** `3`

The most VM health checks that run at once. A value that is absent or 0 means 3, and a negative value stops the broker from loading its configuration. A higher value puts more concurrent load on the BOSH director, so raise it only when the director has the capacity.

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
- `max_concurrent` limits how many checks run at once, so the director sees a bounded load
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
  errand_timeout: 30m           # Longest wait for a BOSH errand (duration)
  update_rollback: false        # Redeploy the last good manifest after a failed update
  cleanup_retry_interval: 15m   # How often owed cleanups of failed provisions are retried
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

HTTP server write timeout in seconds. Maximum time allowed for writing the response. A request that runs a BOSH errand is the one exception, because the broker moves the write deadline out for that request, so the errand can run past `write_timeout` for as long as `errand_timeout` allows.

#### `idle_timeout` (integer)
**Default:** `300`

HTTP server idle timeout in seconds. Maximum time to wait for the next request when keep-alives are enabled.

#### `errand_timeout` (duration)
**Default:** `30m`

The longest the broker holds a request open while a BOSH errand runs, written as a duration such as `45m` or `1h30m`. The broker moves the connection's write deadline out to match, so an errand that runs past `write_timeout` still gets its answer. When the wait runs out, the broker answers 504 with the code `outcome_unknown`, because the errand may still be running.

The broker takes the wait from the first of these that holds a usable value, in this order:

- The runtime setting `errand_timeout`, saved in Vault from the Settings page or with a POST to `/b/blacksmith/settings`

- The environment variable `BLACKSMITH_ERRAND_TIMEOUT`

- This `broker.errand_timeout` setting in the configuration file

- The default of `30m`

Every layer takes a duration, so a bare number is refused, and so is a value of zero or less. A layer that is refused is skipped, and the broker writes a warning to its log that names the layer and the reason. A refused value in the configuration file also fails the load. The Settings page and the settings API show the wait now in effect, and the layer it comes from.

#### `update_rollback` (boolean)
**Default:** `false`

When this is on, the broker redeploys the last good manifest once after a failed instance update. The broker doesn't accept instance updates yet, so for now the setting has no effect, and the Settings page shows it so an operator can choose before updates arrive.

The broker takes the setting from the first of these that holds a usable value, in this order:

- The runtime setting `update_rollback`, saved in Vault from the Settings page or with a POST to `/b/blacksmith/settings` whose body holds `{"update_rollback": true}` or `false`

- The environment variable `BLACKSMITH_UPDATE_ROLLBACK`

- This `broker.update_rollback` setting in the configuration file

- The default of `false`

The environment variable takes `true` or `false`, and the other forms Go's `strconv.ParseBool` reads, such as `1` and `0`. A layer whose value isn't a boolean is skipped, and the broker writes a warning to its log that names the layer and the reason. A value in the configuration file that isn't a boolean fails the load. When the Vault settings can't be read, the rollback counts as off rather than falling through to a lower layer, because a rollback is a redeploy and the broker never redeploys on a guess. The Settings page and the settings API show the value now in effect, and the layer it comes from.

#### `cleanup_retry_interval` (duration)
**Default:** `15m`

When a provision fails after its deploy was sent, the broker deletes the deployment it left and marks the instance's record until the delete goes through. If the director refuses the delete or doesn't answer in time, the mark stays. A sweep in the broker then tries the cleanup again after each interval, and an instance whose cleanup keeps failing waits twice as long after each try, up to six hours. Each try reads the record again under the instance's lock, so once one cleanup has sent the delete, no later try sends it again. A cleanup also gives way to a deprovision of the same instance, so within one broker process only one delete of a deployment is in flight at a time. The lock and the claims that keep it so live in that process's memory, so they don't hold back a second broker that shares the same Vault. A deprovision that arrives while a cleanup of the instance holds the lock waits up to five seconds for it. If the lock is still held, the broker answers 422 with the ConcurrencyError code and records nothing, and it doesn't send the delete itself later, so the delete has to be sent again, for example by running `cf delete-service` again. A cleanup can hold the lock for about five minutes while the director answers it, so for that long every delete of the instance can be refused this way.

Between full scans, the sweep reads only the records of the instances it suspects may owe a cleanup. An instance becomes a suspect when the broker records its failed provision, when a cleanup of it runs, when a poll finds its provision in progress with no index entry, and when the broker starts and finds it owing a cleanup. The sweep reads every instance's record on its first pass and then about every six hours, so an instance it missed is found by the next full scan. When a full scan fails, that pass reads the suspects' records instead and runs their cleanups, and the next pass tries the full scan again. The suspects are kept in memory only, and a restart rebuilds them from that first full scan.

The sweep also settles a provision that a broker stopped in the middle of. When an instance has no index entry and its record shows a provision in progress for longer than the provision timeout of 60 minutes, and no provision in this broker is working on it, the sweep takes the provision over. When the record names the provision's task, the sweep waits for that task to end, and when it names none, the sweep doesn't look one up. Either way, and whatever the task's outcome, the sweep then records the provision as failed, because with its index entry gone the provision can't finish, and it cleans up the deployment as above.

Every cleanup, whether a poll, a start, or the sweep runs it, deletes only a deployment whose name was minted for the instance and is the name recorded for it. Before a provision sends its deploy, its record keeps the deployment's name together with the plan the broker minted that name under, and the name minted for the instance is then exactly that plan's ID, a dash, and the instance ID. That plan is the one the provision asked for, so a plan that later leaves the catalog, or a move of the instance to another plan, doesn't change which deployment the cleanup may delete. A record written before the broker kept the plan is judged by the request details the provision stored under the instance ID and the deployment's name before it sent its deploy. The plan those details hold is the one the provision asked for, and the name must be exactly that plan's ID, a dash, and the instance ID. A name's shape alone never makes a deployment the instance's, so a record whose name has no stored details, or details that don't rebuild the name exactly, is refused. The name recorded for the instance is the name its record holds, or the name its index entry recorded at provision time when the record holds none. When neither names a deployment, the broker asks the director for the provision's task by the record's context ID, and when the request details show that the task's deployment was minted for the instance, the broker writes the name and its plan into the record and then deletes that deployment. A name built from the plan the instance is on now is never used, because after a plan change it isn't the name the deployment was created under. The broker refuses any other cleanup. It writes the refusal to its log with the instance ID and the deployment name, lists the instance under Attention required on the overview, and leaves the mark on the record. The sweep tries a refused cleanup again on its backoff, so the log has the refusal at Error the first time and whenever its deployment or reason changes, and at Warning each time a later try repeats it. The row gives the reason. For a record from before the plan was kept, the reason says whether no request details are stored under the name, the details say a plan that doesn't rebuild it, or the details couldn't be read. For a record with no name, the reason says whether the director couldn't be asked, had no task with the record's context ID, or has a task that names a deployment not minted for the instance, and in that last case the row names the task's deployment. The row stays until the broker reads the record and finds no cleanup owed, so a record it can't read leaves the row in place. Request details it can't read leave a row only when none is held, so the row already there keeps its reason and its time.

Because the name is the plan's ID, a dash, and the instance ID, two instances can be given the same name. Instance `abc` under plan `redis-small-x` and instance `x-abc` under plan `redis-small` are both named `redis-small-x-abc`, and a cleanup that one of them owes would then delete the other's deployment. So before a new provision writes anything, the broker checks that no other instance claims the name it would mint. An instance claims a name when its index entry records that name or its plan mints it, even after the instance was deleted, and when its record owes a cleanup and names that deployment, or names none while the instance's request details are stored under it. A provision of an instance that the index doesn't have also asks the director for the deployment, and it goes on only when the deployment doesn't exist, or when the instance's own record names it for a provision that hasn't failed. When the record names it for a provision that failed, the provision gets 409 Conflict, as an indexed instance whose provision failed does, because the failed deployment must be deleted first. A provision refused because another instance claims the name, or because nothing the broker holds names the deployment, gets a 400 Bad Request answer whose description names the deployment but not the other instance, and the broker logs the other instance's ID at Warning. If the broker can't read the index, the list of instances, or a record it needs, or if the director can't say whether the deployment exists, the provision fails without anything written, and the platform can try it again. A delete of an instance that the index doesn't have makes the same check before it asks the director anything. When another instance claims the name the delete would use, the delete gets 410 Gone with nothing written, because nothing of this instance exists under that name, and the other instance's deployment is left alone. When the check can't read what it needs, the delete fails with nothing written. Cloud Foundry sends GUIDs as instance IDs, and GUIDs never collide this way, so the check for another instance's claim refuses nothing there.

The broker takes the interval from the first of these that holds a usable value, in this order:

- The runtime setting `cleanup_retry_interval`, saved in Vault from the Settings page or with a POST to `/b/blacksmith/settings`

- The environment variable `BLACKSMITH_CLEANUP_RETRY_INTERVAL`

- This `broker.cleanup_retry_interval` setting in the configuration file

- The default of `15m`

Every layer takes a duration from `1m` to `6h`, so a bare number is refused, and so is a value outside that range. Six hours is also the longest the sweep backs off from one instance and the time between its full scans, so a longer interval would only delay both. A layer that is refused is skipped, and the broker writes a warning to its log that names the layer and the reason. A refused value in the configuration file also fails the load. The sweep reads the interval again before each wait, so a saved change takes effect after the current wait without a restart. The Settings page and the settings API show the interval now in effect, and the layer it comes from.

#### `tls` (TLSConfig)
**Default:** See TLS Configuration section

TLS/HTTPS configuration for secure connections. For detailed TLS configuration options, see [docs/tls.md](./tls.md).

The `tls.reuse_after` setting is deprecated. A configuration that sets it still loads, and the value has no effect. The broker logs a warning that names the setting whenever the key is present, even when its value is 0.

#### `compression` (CompressionConfig)

HTTP response compression, with these fields.

```yaml
broker:
  compression:
    enabled: true
    types: ["gzip"]
    level: -1
    min_size: 1024
    content_types:
      - "application/json"
      - "text/html"
```

`enabled` turns compression on or off. `types` lists the encodings the broker offers, which are `gzip`, `deflate`, and `brotli`, and it defaults to `gzip`. `level` runs from 1 to 9, or -1 for the library default. `min_size` is the smallest response in bytes that the broker compresses, and it defaults to 1024. `content_types` lists the MIME types to compress, and when it is empty the broker compresses `text/*` types and `application/json`, among others.

## Cloud Foundry Configuration (`broker.cf`)

The Cloud Foundry settings live under the `broker` section as `broker.cf`. They register the broker with Cloud Foundry and name the API endpoints that the broker view uses. A top-level `cf` key is not read, so a `cf:` section at the top of the file has no effect. [docs/examples/cf-endpoints.yml](examples/cf-endpoints.yml) is a complete example.

```yaml
broker:
  cf:
    enabled: true                                  # Allow CF registration
    broker_url: "https://blacksmith.example.com"   # URL that CF uses to reach this broker
    broker_user: "blacksmith"                      # Username CF sends to this broker
    broker_pass: "change-me"                       # Password CF sends to this broker
    default_name: "blacksmith"                     # Default name for a registration
    apis:
      primary:                                     # API endpoint identifier
        name: "primary-cf"                         # Display name for the CF instance
        endpoint: "https://api.system.example.com" # CF API endpoint URL
        username: "admin"                          # CF admin username
        password: "change-me"                      # CF admin password
        cacert: |                                  # PEM bundle that signs the API and UAA certificates
          -----BEGIN CERTIFICATE-----
          MIIB...
          -----END CERTIFICATE-----
        skip_ssl_validation: false                 # Development only
```

### Optional Fields

#### `enabled` (boolean)
**Default:** `false`

Whether CF registration is enabled.

#### `broker_url` (string)
**Default:** `""`

The public URL that Cloud Foundry uses to reach this broker.

#### `broker_user` and `broker_pass` (string)
**Default:** `""`

The credentials that Cloud Foundry sends to this broker when it registers it.

#### `default_name` (string)
**Default:** `""`

The default broker name for new registrations.

#### `apis` (map of CFAPIConfig)
**Default:** `{}`

Map of Cloud Foundry API endpoint configurations, where each key is a unique identifier for the CF instance. Each endpoint configuration contains connection details for accessing the CF API.

#### CFAPIConfig Fields

##### `name` (string)
**Required**

Display name for the Cloud Foundry instance. This name is shown in the broker view interface to help identify different CF environments.

##### `endpoint` (string)
**Required**

URL of the Cloud Foundry API endpoint, including protocol and domain. This should be the API endpoint URL for the CF deployment.

##### `username` (string)
**Required**

Username for Cloud Foundry API authentication. Should be an admin user with sufficient privileges to manage service brokers and applications.

##### `password` (string)
**Required**

Password for Cloud Foundry API authentication. Store securely and use strong passwords for production environments.

**Security Note:** Consider using environment variables or secure credential management instead of storing passwords directly in configuration files for production deployments.

##### `cacert` (string)
**Default:** `""`

A PEM bundle holding the certificates that sign the CF API and its UAA. Set it when the foundation uses a private certificate authority.

##### `skip_ssl_validation` (boolean)
**Default:** `false`

Turns off TLS verification for this endpoint. Use it for development only, because it exposes the endpoint's credentials to anyone who can intercept the connection.

## Vault Configuration (`vault`)

The `vault` section configures integration with HashiCorp Vault for secrets management.

```yaml
vault:
  address: "https://vault.example.com:8200"  # Required
  token: "vault-token"                       # Optional if using credentials file
  skip_ssl_validation: false                 # Optional
  credentials: "/path/to/vault/creds"        # Optional
  cacert: "/path/to/vault-ca.pem"            # Optional (PEM text or a file path)
  auto_unseal: true                          # Optional (default: true)
  health_check_interval: "15s"               # Optional (default: 15s)
  unseal_cooldown: "30s"                     # Optional (default: 30s)
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

#### `cacert` (string)
**Default:** `""`

The certificate authority that signs Vault's certificate, given either as PEM text or as the path to a PEM file. A value that contains `-----BEGIN` is read as PEM text, and any other value is read as a file path. The broker trusts only this authority, and it does not fall back to the system roots. Set it when Vault uses a private authority. The setting has no effect while `skip_ssl_validation` is `true`, and the broker logs a warning at startup when it ignores the value for that reason. A value that is neither PEM text nor a readable file, or that holds no usable certificate, is an error.

#### `auto_unseal` (boolean)
**Default:** `true`

Enable automatic unsealing when Vault becomes sealed while Blacksmith is running (e.g., Vault service restarted independently). Requires `credentials` to point to a JSON file containing `seal_key` and `root_token` (this is written during initial Vault initialization by Blacksmith).

#### `health_check_interval` (duration string)
**Default:** `"15s"`

Interval for the background health watcher to check Vault’s status. If Vault is detected sealed, Blacksmith will attempt to unseal it using the configured credentials.

#### `unseal_cooldown` (duration string)
**Default:** `"30s"`

Minimum time between auto‑unseal attempts to avoid repeated hammering when Vault is unavailable or failing to unseal. Applies per‑process and is enforced even if multiple operations notice the sealed state concurrently.

#### `history_retention` (object)

The section takes `enabled`, `retention_days`, and `max_entries`. The configuration loader reads these keys, but the broker does not act on them in this version, so setting them changes nothing.

### Handling Vault Restarts (Auto‑Unseal)

When Vault restarts independently of the broker, it may come back sealed. With `auto_unseal: true`, Blacksmith will:

- Detect sealed/unavailable responses during Vault operations and automatically attempt to unseal using the `credentials` file, then retry once.
- Run a background health watcher at `health_check_interval` that proactively unseals Vault if sealed.

The `credentials` file must be a JSON with `seal_key` and `root_token`. Blacksmith writes this during the initial `vault.init` phase.

#### Troubleshooting Auto‑Unseal

- Logs to look for:
  - `vault auto-unseal` and `vault watcher` messages indicating detection and unseal attempts.
  - `vault auto-unseal successful` when unseal completes.
  - Errors like `failed to load seal key for auto-unseal` or `vault is sealed but no credentials path configured` indicate misconfiguration.
- Verify configuration:
  - `vault.credentials` points to a readable file with `seal_key` and `root_token`.
  - `vault.auto_unseal` is `true` (default).
  - Adjust `vault.health_check_interval` and `vault.unseal_cooldown` if needed.
- Environment and connectivity:
  - Ensure `VAULT_ADDR` and network connectivity to Vault are correct.
  - If TLS issues occur, confirm CA configuration or set `skip_ssl_validation` only in dev.
- Manual recovery:
  - If auto‑unseal lacks permissions or keys, manually unseal Vault and correct the credentials file path/contents.

## BOSH Configuration (`bosh`)

The `bosh` section configures integration with the BOSH director for service deployment management.

```yaml
bosh:
  address: "https://bosh.example.com:25555"             # Required
  username: "admin"                                     # Required
  password: "admin-password"                            # Required
  cacert: |                                             # Required (PEM text)
    -----BEGIN CERTIFICATE-----
    MIIB...
    -----END CERTIFICATE-----
  allow_http: false                                     # Optional (default false)
  cloud-config: "/var/vcap/jobs/blacksmith/config/cloud-config.yml"  # Optional
  network: "blacksmith"                                 # Optional
  max_connections: 4                                    # Optional (connection pool size)
  max_batch_connections: 10                             # Optional (batch operation pool size)
  connection_timeout: 300                               # Optional (timeout in seconds)
  request_timeout: 60                                   # Optional (timeout in seconds)
  task_watch_max: 21600                                 # Optional (limit in seconds)
  director_workers: 4                                   # Optional (the director's worker count; 0 means 4)
  stemcells:                                            # Optional
    - name: "bosh-warden-boshlite-ubuntu-noble-go_agent"
      version: "1.25"
      url: "https://bosh.io/d/stemcells/bosh-warden-boshlite-ubuntu-noble-go_agent"
      sha1: "a743eafa91a69306da0b7d420ceb3621a277e9c5"
  releases:                                  # Optional
  - name: rabbitmq-forge
    version: 1.4.3
    url: https://github.com/blacksmith-community/rabbitmq-forge-boshrelease/releases/download/v1.4.3/rabbitmq-forge-1.4.3.tgz
    sha1: sha256:d8cac206354388b3cd521fb50cc861991e3ac12f7687e1fc11865d57ac801fc1
  - name: redis-forge
    version: 1.3.3
    url: https://github.com/genesis-community/redis-forge-boshrelease/releases/download/v1.3.3/redis-forge-1.3.3.tgz
    sha1: sha256:4ec3e2f921d9170bbdd3e19031bd407cab71b00e818bd9927f524f8b0dba95fe
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

#### `cacert` (string)
**Required**

PEM text of the CA certificate that signed the BOSH director's certificate. This is the certificate itself, not the path to a file. Blacksmith always verifies the director's certificate against it, and the same CA must also sign UAA's certificate, because the client trusts this one CA for both. A missing value, or one that holds no certificate that parses, stops the broker from booting.

### Optional Fields

#### `allow_http` (boolean)
**Default:** `false`

Allows `bosh.address` to use plain `http://`. With the default, an `http://` address stops the broker from booting, because the director's credentials and every request to it would cross the network unencrypted. When this is `true`, the broker boots and logs a warning that it talks to the director over plain HTTP. The CA in `cacert` is still required. An `https://` address, or one with no scheme, is not affected by this setting. The BOSH release sets it through the `bosh.allow_http` property.

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

#### `max_connections` (integer)
**Default:** `4`

Maximum number of concurrent connections to the BOSH director API. This creates a connection pool that limits the number of simultaneous BOSH API operations to prevent overwhelming the director with too many concurrent requests.

Connection pooling helps improve stability and performance when Blacksmith needs to make many BOSH API calls, such as during VM monitoring, reconciliation, or when handling multiple service operations simultaneously.

#### `max_batch_connections` (integer)
**Default:** `10`

Maximum number of concurrent director connections for batch operations, such as upgrades, certificate jobs, and command runs. Batch operations use a pool of their own, so a large batch does not starve the connections that the console and the reconciler use. [Run command](run-command.md#parallel-runs) explains how its parallel option relates to the director's workers.

#### `connection_timeout` (integer)
**Default:** `300` (5 minutes)

Timeout in seconds for acquiring a connection slot from the pool. When all connections are in use, new requests will wait up to this duration for a connection to become available. If the timeout is exceeded, the operation will fail with a timeout error.

This setting helps prevent operations from waiting indefinitely when the BOSH director is under heavy load or experiencing issues.

#### `request_timeout` (integer)
**Default:** `60` (1 minute)

Timeout in seconds for one HTTP request to the BOSH director, which also bounds how long the director may take to start answering it. A request that gets no answer in that time is an attempt that failed. Blacksmith retries a read up to 3 times, each attempt with its own timeout, so a director that never answers can hold a read for four times this value, plus the wait between attempts. The retry count is fixed and has no setting in the `bosh` section. When the attempts are exhausted, the error names this setting, how many attempts timed out, and the director's address. A negative value is rejected when the configuration is read.

#### `task_watch_max` (integer)
**Default:** `21600` (6 hours)

The limit in seconds on how long Blacksmith keeps a pool slot or a batch slot for a director task it can't settle. It applies in two places. When every read of a task Blacksmith follows fails, Blacksmith keeps reading for this long before it frees the task's slot, and a read that succeeds starts the count again. When the director's answer to a restart, stop, start, or recreate is lost, Blacksmith holds the slot until a successful check, made at least 15 minutes after the answer was lost, finds no task running on the deployment. If that never happens, Blacksmith frees the slot once this limit has passed since the answer was lost, with an Error log that names the deployment. Failed or refused checks don't free that slot early, and they don't extend the limit. A value of 0 means the default, and a negative value is rejected when the configuration is read.

#### `director_workers` (integer)
**Default:** `0` (the broker assumes 4)

How many workers the BOSH director runs. The Run command batch operation uses this number to bound its parallel option, because the SSH setup and cleanup behind each node are director tasks that each hold a worker while they run. The highest `max_in_flight` a run may ask for is one less than the worker count, so the default of 4 allows up to 3. Set it to match the `director.workers` value of your director when that value is not 4. The director does not report its worker count through its API, so Blacksmith cannot read it for you. A value of 0 means the default of 4, a value of 1 is rejected when the configuration is read because no parallel run could fit under it, and a negative value is rejected as well. See [Run command](run-command.md#parallel-runs) for how the bound is applied.

### Connection Pool Monitoring

The BOSH connection pool provides real-time metrics available via the internal API endpoint `/b/bosh/pool-stats`:

```json
{
  "max_connections": 4,
  "active_connections": 2,
  "queued_requests": 1,
  "total_requests": 1547,
  "rejected_requests": 0
}
```

**Metrics explained:**
- `max_connections`: Maximum number of concurrent connections configured
- `active_connections`: Number of connections currently in use
- `queued_requests`: Number of requests waiting for an available connection
- `total_requests`: Total number of requests processed since startup
- `rejected_requests`: Number of requests rejected due to timeout

### SSH Configuration (`ssh`)

> **Note:** SSH configuration has moved from `bosh.ssh` to the top-level `ssh` key. The old location under `bosh.ssh` is deprecated but still supported for backward compatibility.

The `ssh` section configures SSH connection behavior for all SSH operations including service instances, Blacksmith deployment, and interactive terminal sessions.

```yaml
# New location (recommended)
ssh:
  enabled: true                    # SSH functionality (default: true)
  ui_terminal_enabled: true        # SSH terminal UI in the web interface (default: true)
  keep_alive: 10                   # Keep-alive interval in seconds (default: 10)
  timeout: 600                     # Overall timeout in seconds (default: 600)
  connect_timeout: 30              # Connection timeout in seconds (default: 30)
  session_init_timeout: 60         # Session initialization timeout in seconds (default: 60)
  output_read_timeout: 2           # Output read timeout in seconds (default: 2)
  max_concurrent: 10               # Maximum concurrent SSH sessions (default: 10)
  max_output_size: 1048576         # Maximum output size in bytes (default: 1MB)
  retry_attempts: 3                # Number of retry attempts (default: 3)
  retry_delay: 5                   # Delay between retries in seconds (default: 5)
  websocket:
    enabled: true
    max_sessions: 50

# Old location (deprecated but still supported)
bosh:
  ssh:
    # Same configuration options as above
```

**Migration Note:** If you have SSH configuration under `bosh.ssh`, it will be automatically migrated to the top-level `ssh` key. You should update your configuration file to use the new location.

#### `enabled` (boolean)
**Default:** `true`

The key defaults to `true` when it is absent from the file, and an explicit `false` turns SSH off for the whole broker. When it is `false`, the broker refuses every SSH-backed entry point with a 403 answer, the error code `ssh_disabled`, and a message that starts "SSH is disabled". The entry points fall into the following five groups.

- The terminal and command routes

  The two terminal WebSockets, `/b/blacksmith/ssh/stream` and `/b/{id}/ssh/stream`, along with `/b/ssh/status` and `/b/{id}/ssh/execute`.

- The RabbitMQ routes

  The SSH, `rabbitmqctl`, and plugins routes under `/b/{id}/rabbitmq/`.

- The trusted-certificate routes

  `GET`, `POST`, and `DELETE` on `/b/{id}/certificates/trusted`, and the `/trusted/file` route beneath it.

- The batch operations

  `/b/upgrade/commands/preview`, and `POST /b/upgrade/tasks` with the kind `command`. Certificate rotation in patch mode is refused too, which covers its preview, its create, and its preflight.

- The runners

  The patch and command runners open no session either, so a task created before the switch went off opens none.

`GET /b/upgrade/commands/availability` reports commands as unavailable with the reason `ssh_disabled`, and `GET /b/config/ssh/ui-terminal-status` reports `enabled` as `false` when either this key or `ui_terminal_enabled` is off. The `ui_terminal_enabled` key is narrower, because it turns off only the two terminal WebSockets.

#### `ui_terminal_enabled` (boolean)
**Default:** `true`

This setting controls whether the web interface offers the SSH terminal, and it affects only the web interface, never the backend SSH functionality. The key defaults to `true` when it is absent from the file, and an explicit `false` turns the terminal off. With the terminal off, the web interface hides its SSH terminal buttons, the two terminal WebSockets, `/b/blacksmith/ssh/stream` and `/b/{id}/ssh/stream`, answer 403 Forbidden, and `GET /b/config/ssh/ui-terminal-status` reports `enabled` as `false`. Every other SSH-backed route keeps working, so a deployment that wants no web-based terminal access can set this key to `false` and keep the rest of SSH. The `enabled` key above is the one that turns all of SSH off.

#### `keep_alive` (integer)
**Default:** `10`

Keep-alive interval in seconds for SSH connections. This sends periodic packets to prevent connection timeout. Lower values provide more responsive connections but increase network traffic.

#### `timeout` (integer)
**Default:** `600` (10 minutes)

Overall timeout for SSH operations in seconds.

#### `connect_timeout` (integer)
**Default:** `30`

Timeout for establishing SSH connections in seconds.

#### `session_init_timeout` (integer)
**Default:** `60`

Timeout for SSH session initialization in seconds.

#### `output_read_timeout` (integer)
**Default:** `2`

Timeout for reading SSH command output in seconds.

#### `max_concurrent` (integer)
**Default:** `10`

Maximum number of concurrent SSH sessions allowed.

#### `max_output_size` (integer)
**Default:** `1048576` (1MB)

Maximum size of SSH command output in bytes.

#### `retry_attempts` (integer)
**Default:** `3`

Number of retry attempts for failed SSH operations.

#### `retry_delay` (integer)
**Default:** `5`

Delay between retry attempts in seconds.

#### `websocket` (object)

The settings for the SSH terminal that the console opens over a WebSocket. When a setting is left out or set to 0, the broker uses the default shown.

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `true` | Serve the WebSocket terminal. A `false` turns it off. |
| `read_buffer_size` | `4096` | Read buffer size in bytes |
| `write_buffer_size` | `4096` | Write buffer size in bytes |
| `handshake_timeout` | `10` | Seconds allowed for the upgrade handshake |
| `max_message_size` | `32768` | Largest message in bytes |
| `ping_interval` | `5` | Seconds between pings |
| `pong_timeout` | `10` | Seconds to wait for a pong |
| `max_sessions` | `50` | Most WebSocket sessions at one time |
| `session_timeout` | `1800` | Seconds an idle session may sit without activity before it is cleaned up, and also the longest a session can last in total |
| `enable_compression` | `false` | Compress WebSocket messages |

#### Host key verification

Host key verification has no settings. When Blacksmith sets up an SSH user on a VM, the BOSH director reports that VM's host key, and Blacksmith connects only if the VM presents that key. A VM that presents another key, or offers no key of the type the director reported, is refused before any login. The error names the deployment, the instance, and the fingerprint of the reported key, together with the fingerprint of the key the VM presented or the key types it offered. Earlier versions read two host key settings in this section. A configuration file that still sets them loads, and Blacksmith ignores them.

#### SSH gateways

Blacksmith connects to each VM directly, so the broker needs a network path to port 22 on the service VMs. When the BOSH director's `default_ssh_options` name a gateway, Blacksmith refuses the SSH setup and removes the user it created, because the gateway would refuse the throwaway key Blacksmith logs in with. The error names the gateway.

#### SSH users at shutdown

When the broker shuts down, it stops opening SSH sessions once its HTTP servers stop. A setup that starts after that point fails with an error saying the broker is shutting down. A setup that was already running opens no session either, and when it finishes, the broker asks the director to remove its user and reports the same error. The broker then asks the director to remove the user of every open session, sending all of these requests at once and allowing them five seconds in total. It does not wait for the removal tasks to finish. Each SSH user keeps its record in Vault until a removal is seen to finish, so the cleanup sweep at the next start removes any user that a shutdown missed, including users whose setup was still running when the broker stopped.

## Commands Configuration (`commands`)

The `commands` section controls the Run command batch operation, which runs one command on every VM of the deployments behind the service instances an operator selects. The operation is described in [Run command](run-command.md). Every field in this section is optional.

```yaml
commands:
  enabled: true          # the whole operation; unset means on
  allow_root: false      # runs as root; unset means off
  credential:            # optional second credential; unset means none
    key: ""              # when set, every preview, create, and output read must send it
```

### Optional Fields

#### `enabled` (boolean)
**Default:** `true`

Turns the whole operation on or off. When it is off, the broker refuses previews, creates, and output reads with a 403 `commands_disabled` error, and the broker UI disables the Run command choice. Turning it off also stops any stored command task at the next authorization check it reaches, so a task that is part way through a run does not start another node.

#### `allow_root` (boolean)
**Default:** `false`

Allows an operator to run a command as root. With the default, operators can run as `vcap` only, and a request to run as root is refused with a 403 `root_disabled` error. We keep this off by default so that a deployment turns root on deliberately. Until then the root choice is greyed out in the UI.

A command that runs as `vcap` can't use `sudo`, which fails with `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.`, so privileged work needs root, and the operator then leaves `sudo` out of the command. This differs from `bosh ssh`, where `sudo` works because the director creates a temporary `bosh_*` user with passwordless sudo. Run command deliberately steps down to `vcap` with no new privileges, so that `allow_root: false` keeps root off. Root can change anything on the VM, and nothing the broker does can contain what it starts, so we recommend `vcap` whenever it can do the job. A deployment through the Blacksmith kit turns root on with `commands_allow_root: true` in the environment file's `params:` section, followed by a redeploy.

The switch decides only whether a root run can start. Turning it off stops every stored root task at the next authorization check it reaches, but it never hides the output of a root run that already happened, because reading that output is an audit read. Root runs are never allowed while `enabled` is false.

#### `credential.key` (string)
**Default:** `""` (no second credential)

A second credential, shared by everyone who may run commands. When it is set, every command preview, create, and output read must send it in the `X-Blacksmith-Command-Key` request header, and the broker refuses a missing or wrong key with a 401 `command_credential_required` error. The same key also guards the detail of a command task, because that detail carries the command text, and it guards a resume that sends a command. The key travels only in the header and never in a JSON body, the broker compares it in constant time, and the request-header debug log redacts it. Treat the value as a secret, and keep it out of version control as we do the broker password.

## CredHub Configuration (`credhub`)

The `credhub` section lets the broker delete the BOSH-generated variables that a deprovisioned instance leaves in the director's CredHub. It is off unless `cleanup.enabled` is true, and an empty section changes nothing.

```yaml
credhub:
  url: "https://credhub.example.com:8844"
  ca_cert: |
    -----BEGIN CERTIFICATE-----
    MIIB...
    -----END CERTIFICATE-----
  client_id: "blacksmith"
  client_secret: "change-me"
  director_name: "my-director"
  uaa_url: "https://uaa.example.com:8443"
  cleanup:
    enabled: true
    sweep: "off"
    protected_deployments: []
```

### Fields

#### `url` (string)
**Required when cleanup is enabled**

The CredHub address. It must be an `https` URL.

#### `ca_cert` (string)
**Required when cleanup is enabled**

PEM text of the certificate authority that signs CredHub's certificate. The broker refuses a value that holds no PEM certificate.

#### `client_id` and `client_secret` (string)
**Required when cleanup is enabled**

The UAA client that the broker uses to authenticate to CredHub.

#### `director_name` (string)
**Required when cleanup is enabled**

The director's name, which is the first segment of every CredHub path the director writes. It must not contain `/`, `*`, or `%`.

#### `uaa_url` (string)
**Default:** `""`

The UAA address, which must be an `https` URL when it is set.

#### `cleanup.enabled` (boolean)
**Default:** `false`

Turns the cleanup on. When it is on, the broker checks the whole block at startup and reports every problem at once.

#### `cleanup.sweep` (string)
**Default:** `"off"`

Controls a separate pass over CredHub that finds credentials belonging to no known deployment. `off` skips the pass, `dry-run` reports what it would delete, and `delete` deletes it.

#### `cleanup.protected_deployments` (array of strings)
**Default:** `[]`

Deployment names whose credentials the cleanup and the sweep never touch.

## Certificates Configuration (`certificates`)

The `certificates` section holds three switches for the certificate features. Each one takes an `enabled` boolean.

```yaml
certificates:
  inventory:
    enabled: true
  rotation:
    enabled: false
  patch_in_place:
    enabled: false
```

#### `inventory.enabled` (boolean)
**Default:** `true`

Runs the certificate inventory, which scans service instances for certificates and reports their expiry.

#### `rotation.enabled` (boolean)
**Default:** `false`

Allows certificate rotation to change anything. Until it is on, rotation previews work and no job changes a certificate.

#### `patch_in_place.enabled` (boolean)
**Default:** `false`

Allows patching a certificate on a VM. It needs `rotation.enabled` as well, and the feature stays off when only this switch is on.

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

Blacksmith talks to the director through its own API client, which reads `bosh.address`, `bosh.username`, `bosh.password`, and `bosh.cacert` from the configuration, so it exports no `BOSH_ENVIRONMENT`, `BOSH_CLIENT`, `BOSH_CLIENT_SECRET`, or `BOSH_CA_CERT`.

## Reconciler Configuration (`reconciler`)

The `reconciler` section configures the deployment reconciler that monitors and maintains service instance state consistency between Vault and BOSH.

```yaml
reconciler:
  enabled: true                    # Enable reconciler
  interval: "5m"                   # Reconciliation interval (Go duration or bare seconds, e.g. 3600)
  max_concurrency: 4              # Maximum concurrent reconciliations
  batch_size: 10                  # Batch size for processing instances
  retry_attempts: 3               # Retry attempts for failed operations
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

How often the reconciler runs to check for inconsistencies. It accepts Go durations such as `"30s"`, `"5m"`, and `"1h"`, and it also accepts a bare number of seconds such as `3600`. When a value can't be parsed, Blacksmith logs a warning that names the setting, shows the bad value, and says which interval it uses instead. The shortest interval the reconciler runs at is ten seconds, so a shorter value is raised to ten seconds with a warning that names the setting, shows the value, and gives the minimum.

An interval saved through the UI or the settings API overrides this value, and so does the `BLACKSMITH_RECONCILER_INTERVAL` environment variable. A saved interval is stored in Vault, so it persists across restarts and redeploys, and it outranks the environment variable. See [Interval Format and Precedence](reconciler.md#interval-format-and-precedence).

#### `max_concurrency` (integer)
**Default:** `4`

Maximum number of service instances that can be reconciled simultaneously.

#### `batch_size` (integer)
**Default:** `10`

Number of instances to process in each reconciliation batch.

#### `retry_attempts` (integer)
**Default:** `3`

Number of retry attempts for failed reconciliation operations.

#### `retry_delay` and `cache_ttl` (string)
The loader reads both keys, but the reconciler does not use either one, so they have no effect and no environment variable. The delay between retries starts at one second and doubles up to 30 seconds, and the environment variables `BLACKSMITH_RECONCILER_RETRY_INITIAL_DELAY` and `BLACKSMITH_RECONCILER_RETRY_MAX_DELAY` change it. The deployment cache lives for a fixed five minutes. See [reconciler.md](reconciler.md#tuning-with-environment-variables).

#### `debug` (boolean)
**Default:** the value of the top-level `debug` setting

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
  timeout: 300
  max_concurrent: 3

reconciler:
  enabled: true
  interval: "5m"
  max_concurrency: 4
  batch_size: 10
  retry_attempts: 3
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
  cf:
    enabled: true
    broker_url: "https://blacksmith.example.com"
    broker_user: "cf-broker"
    broker_pass: "secure-password"
    apis:
      primary:
        name: "primary-cf"
        endpoint: "https://api.system.example.com"
        username: "admin"
        password: "change-me"
      production:
        name: "production-cf"
        endpoint: "https://api.cf.production.com"
        username: "cf-admin"
        password: "secure-password"

vault:
  address: "https://vault.example.com:8200"
  skip_ssl_validation: false
  credentials: "/etc/blacksmith/vault-creds"

bosh:
  address: "https://bosh.example.com:25555"
  username: "blacksmith"
  password: "bosh-password"
  cacert: |
    -----BEGIN CERTIFICATE-----
    MIIB...
    -----END CERTIFICATE-----
  network: "services"
  max_connections: 4
  connection_timeout: 300
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

# Blacksmith TLS Configuration

Blacksmith provides native TLS/SSL support for secure HTTPS communication, eliminating the need for external reverse proxies like Nginx. This document explains how to configure and use TLS with Blacksmith.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Certificate Management](#certificate-management)
- [Security Settings](#security-settings)
- [Certificate Inventory and Rotation](#certificate-inventory-and-rotation)
- [Examples](#examples)
- [Migration from Nginx](#migration-from-nginx)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)

## Overview

Blacksmith's native TLS implementation provides:

- **Native Go TLS termination** - No external dependencies
- **Dual server mode** - HTTP and HTTPS servers running simultaneously
- **Automatic redirects** - HTTP to HTTPS redirection when TLS is enabled
- **Configurable security** - TLS protocols, TLS 1.2 cipher suites, and timeouts
- **Production ready** - Graceful shutdown, proper error handling, and logging

## Architecture

### HTTP-Only Mode (TLS Disabled)
```
Client → Blacksmith HTTP Server (Port 3000)
```

### HTTPS Mode (TLS Enabled)
```
Client → Blacksmith HTTP Server (Port 3000) → 301 Redirect → HTTPS (Port 443)
Client → Blacksmith HTTPS Server (Port 443) → Native TLS Handling
```

The HTTPS server turns HTTP/2 off, so every connection uses HTTP/1.1. The broker does this on purpose, because a browser that negotiated HTTP/2 would try to upgrade a WebSocket on an HTTP/2 stream, and the server cannot hijack that stream.

### Key Components

- **HTTP Server**: Handles normal traffic (TLS disabled) or redirects (TLS enabled)
- **HTTPS Server**: Handles TLS-encrypted traffic when TLS is enabled
- **TLS Configuration**: Manages protocols, cipher suites, and certificates
- **Certificate Validation**: Loads the certificate and key pair on startup and refuses to start when it does not load

## Configuration

TLS is configured in the `broker.tls` section of your Blacksmith configuration file:

```yaml
broker:
  username: blacksmith
  password: your-password
  port: 3000              # HTTP port
  bind_ip: 0.0.0.0        # Bind to all interfaces
  
  # TLS Configuration
  tls:
    enabled: true
    port: 443
    certificate: /path/to/certificate.pem
    key: /path/to/private-key.pem
    protocols: "TLSv1.2 TLSv1.3"
    ciphers: "ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384:HIGH:!MD5:!aNULL:!EDH"
```

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable/disable TLS support |
| `port` | string | `"443"` | HTTPS port to listen on |
| `certificate` | string | - | Path to PEM-encoded certificate file |
| `key` | string | - | Path to PEM-encoded private key file |
| `protocols` | string | `"TLSv1.2 TLSv1.3"` | Space-separated list of TLS protocols |
| `ciphers` | string | See [Security Settings](#security-settings) | Colon-separated list of cipher suites, which applies to TLS 1.2 only |
| `reuse_after` | integer | none | Deprecated. The broker accepts the key, logs one warning at startup when it is set, and ignores it. It will be removed. |

The broker reads the certificate and key once at startup. After we replace either file, we have to restart the broker before the HTTPS listener serves the new certificate.

## Certificate Management

### Certificate Requirements

- **Format**: PEM-encoded X.509 certificates
- **Key**: RSA or ECDSA private keys
- **File Access**: Blacksmith must have read access to both files
- **Validation**: Certificate and key must be a valid pair

### Generating Self-Signed Certificates (Development)

For development and testing, you can generate self-signed certificates:

```bash
# Generate private key
openssl genrsa -out blacksmith-key.pem 2048

# Generate certificate signing request
openssl req -new -key blacksmith-key.pem -out blacksmith.csr \
  -subj "/C=US/ST=State/L=City/O=Organization/CN=localhost"

# Generate self-signed certificate
openssl x509 -req -in blacksmith.csr -signkey blacksmith-key.pem \
  -out blacksmith-cert.pem -days 365
```

### Production Certificates

For production use, obtain certificates from a trusted Certificate Authority (CA) or use Let's Encrypt:

```bash
# Using certbot for Let's Encrypt
certbot certonly --standalone -d your-domain.com

# Certificates will be in:
# /etc/letsencrypt/live/your-domain.com/fullchain.pem
# /etc/letsencrypt/live/your-domain.com/privkey.pem
```

### Certificate Validation

Blacksmith loads the certificate and key pair on startup, and that load checks three things:

- **File existence**: Certificate and key files must exist
- **File format**: Must be valid PEM-encoded files
- **Key matching**: Private key must match the certificate

Blacksmith does not check the expiry date at startup. A broker with an expired certificate starts normally, and its clients then reject the certificate. The [certificate inventory](#certificate-inventory-and-rotation) reports expiry ahead of time, so we use it to catch a certificate before it expires.

If the load fails, Blacksmith logs the error and exits.

## Security Settings

### TLS Protocols

Supported TLS protocol versions:

```yaml
tls:
  protocols: "TLSv1.2 TLSv1.3"  # Recommended
  # protocols: "TLSv1.2"        # TLS 1.2 only
  # protocols: "TLSv1.3"        # TLS 1.3 only
```

**Recommendations**:
- Use `TLSv1.2 TLSv1.3` for maximum compatibility
- Avoid TLS 1.0 and 1.1 (deprecated and insecure)
- TLS 1.3 provides better performance and security

### Cipher Suites

The default value of `ciphers` is the following.

```
ECDHE-RSA-AES128-SHA256:AES128-GCM-SHA256:HIGH:!MD5:!aNULL:!EDH
```

The broker reads the `ciphers` value with these rules, in this order.

- Any value that contains the text `HIGH` gives Go's default cipher suites, and the rest of the value is ignored. That includes the default above, so a broker with no `ciphers` setting runs Go's defaults.

- An empty value also gives Go's default suites.

- Any other value is split on colons. The broker skips entries that start with `!`, and it looks each remaining name up in a fixed table of OpenSSL-style names, such as `ECDHE-RSA-AES128-GCM-SHA256` or `AES256-SHA`. It logs an error for each name it does not know. When no name is valid, the broker falls back to Go's default suites.

- The list applies to TLS 1.2 only. Go does not let a config choose TLS 1.3 suites, so names such as `TLS_AES_256_GCM_SHA384` are accepted in the list but have no effect on TLS 1.3 connections.

To pin the suites, we leave out `HIGH` and name them. For example, the following value allows two TLS 1.2 suites with forward secrecy.

```yaml
ciphers: "ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384"
```

### Session Reuse

The `reuse_after` key is deprecated. The broker never read it, because Go's TLS server handles session resumption on its own. When we set the key, the broker logs one warning at startup that says the key has no effect and will be removed. We delete the key from our config, and a config that leaves it unset stays silent.

## Certificate Inventory and Rotation

Blacksmith keeps an inventory of the certificates that matter to the broker and to each service instance, and it can rotate them. Three switches in the `certificates` section of the config control the features.

```yaml
certificates:
  inventory:
    enabled: true
  rotation:
    enabled: false
  patch_in_place:
    enabled: false
```

| Switch | Default | Effect |
|--------|---------|--------|
| `certificates.inventory.enabled` | `true` | Runs the certificate inventory. Every inventory route and every rotation task needs it. |
| `certificates.rotation.enabled` | `false` | Lets a certificate task change anything. With it off, we can still read the inventory. |
| `certificates.patch_in_place.enabled` | `false` | Allows the patch mode of rotation. It works only when `certificates.rotation.enabled` is also `true`. |

A rotation task also needs a configured CredHub client, because the certificates are regenerated in CredHub.

### Inventory

The inventory scans each service instance's deployment on a schedule and records every certificate it finds, with its expiry date. It also scans the broker's own certificates. That scan covers the HTTPS listener certificate when `broker.tls.enabled` is `true`, the CA certificates the broker is configured with for BOSH, CredHub, Vault, and each CF API, the services CA, and the BOSH trusted certificates on the broker VM. The broker's own entries use the reserved instance ID `_broker`.

The routes live under `/b/certificates/`.

| Route | Purpose |
|-------|---------|
| `GET /b/certificates/inventory` | Lists the certificates, with filters and a csv or json export |
| `POST /b/certificates/scan` | Starts a scan, and takes an optional `instance_ids` list in its JSON body |
| `GET /b/certificates/instances/{id}` | Scans one instance and returns its certificates |
| `GET /b/certificates/items/{id}` | Returns one certificate |
| `GET /b/certificates/settings` | Reads the runtime settings |
| `PUT /b/certificates/settings` | Saves the runtime settings |

The runtime settings are saved in Vault and can be changed without a redeploy. They start with these defaults, which are the warning at 30 days, the critical alert at 7 days, a scan every 60 minutes, and four scans at a time.

### Rotation

`POST /b/certificates/rotation/preview` builds a preview of what a rotation would change, and a certificate task then uses that preview. We create the task with `POST /b/upgrade/tasks` and a body whose `kind` is `certificates`. A task runs in one of three modes.

- **redeploy**
  regenerates the certificates in CredHub and redeploys the instance.

- **patch**
  regenerates the certificates and writes them onto the VMs in place, which avoids a redeploy. It needs `certificates.patch_in_place.enabled`, and the request has to name a converge policy.

- **converge**
  redeploys so the VMs pick up certificate versions that already exist in CredHub. It regenerates nothing.

A patch is a temporary repair, because BOSH can restore the old certificate until the instance converges. The converge policy of a patch task says what happens next. With `pending`, the task leaves the redeploy to a later task or deploy. With `immediate`, the task redeploys as soon as the VMs are patched.

When a switch is off, the broker refuses the task with an error that names the switch to turn on.

### Expiry Monitoring

The inventory is the built-in way to watch certificate expiry. It reports each certificate against the warning and critical thresholds in its settings, so we use it in place of an external check for the certificates it covers.

## Examples

### Basic HTTPS Setup

```yaml
broker:
  username: blacksmith
  password: secure-password
  port: 80
  bind_ip: 0.0.0.0
  
  tls:
    enabled: true
    port: 443
    certificate: /etc/ssl/certs/blacksmith.pem
    key: /etc/ssl/private/blacksmith.key
```

### High Security Configuration

```yaml
broker:
  username: blacksmith
  password: secure-password
  port: 80
  bind_ip: 0.0.0.0
  
  tls:
    enabled: true
    port: 443
    certificate: /etc/ssl/certs/blacksmith.pem
    key: /etc/ssl/private/blacksmith.key
    protocols: "TLSv1.3"
```

### Development Configuration

```yaml
broker:
  username: blacksmith
  password: blacksmith
  port: 8080
  bind_ip: 127.0.0.1
  
  tls:
    enabled: true
    port: 8443
    certificate: ./dev-cert.pem
    key: ./dev-key.pem
    protocols: "TLSv1.2 TLSv1.3"
```

### HTTP-Only Configuration

```yaml
broker:
  username: blacksmith
  password: blacksmith
  port: 3000
  bind_ip: 0.0.0.0
  
  tls:
    enabled: false  # TLS disabled
```

## Migration from Nginx

If you're migrating from an Nginx-based TLS setup:

### Before (Nginx + Blacksmith)

```nginx
server {
    listen 443 ssl;
    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;
    
    location / {
        proxy_pass http://127.0.0.1:3001;
    }
}
```

```yaml
# Old Blacksmith config
broker:
  port: 3001
  bind_ip: 127.0.0.1
```

### After (Native TLS)

```yaml
# New Blacksmith config
broker:
  port: 80
  bind_ip: 0.0.0.0
  
  tls:
    enabled: true
    port: 443
    certificate: /path/to/cert.pem
    key: /path/to/key.pem
```

### Migration Steps

1. **Update Blacksmith configuration** to include TLS settings
2. **Change bind_ip** from `127.0.0.1` to `0.0.0.0`
3. **Test the configuration** with Blacksmith directly
4. **Remove Nginx configuration** once verified
5. **Update firewall rules** if necessary

## Troubleshooting

### Common Issues

#### Certificate Validation Errors

```
ERROR [*] TLS configuration error: failed to load TLS certificate/key pair
```

**Solutions**:
- Verify certificate and key file paths
- Check file permissions (Blacksmith needs read access)
- Ensure certificate and key are a matching pair
- Validate PEM format

#### Port Binding Issues

```
ERROR [*] HTTP server failed: listen tcp :443: bind: permission denied
```

**Solutions**:
- Run as root/administrator for ports < 1024
- Use alternative ports (8080/8443) for non-root operation
- Configure port forwarding or reverse proxy

#### TLS Handshake Failures

**Symptoms**: Clients cannot connect or receive SSL errors

**Solutions**:
- Check TLS protocol compatibility
- Verify cipher suite support
- Ensure certificate is trusted by clients
- Check for intermediate certificate issues

### Debugging

Enable debug logging to troubleshoot TLS issues:

```yaml
debug: true
```

Check startup logs for TLS configuration:
```
INFO [*] TLS enabled - certificate: /path/to/cert.pem, key: /path/to/key.pem
INFO [*] HTTP server on 0.0.0.0:80 will redirect to HTTPS port 443
INFO [*] HTTPS server will listen on 0.0.0.0:443
```

### Testing TLS Configuration

```bash
# Test HTTP redirect
curl -v http://localhost:3000/

# Test HTTPS (ignore self-signed certificate warnings)
curl -k -v https://localhost:443/

# Test with specific TLS version
curl -k --tlsv1.2 https://localhost:443/

# Check certificate details
openssl s_client -connect localhost:443 -showcerts
```

## Best Practices

### Security

1. **Use strong cipher suites** - Name ECDHE and AES-GCM suites without `HIGH`, or accept Go's defaults
2. **Disable weak protocols** - Avoid TLS 1.0 and 1.1
3. **Regular certificate updates** - Watch expiration dates in the certificate inventory
4. **Secure key storage** - Protect private key files
5. **Test configurations** - Validate with SSL Labs or similar tools

### Performance

1. **Use efficient cipher suites** - Go's defaults already prefer AES-GCM and ChaCha20-Poly1305
2. **Consider TLS 1.3** - Better performance than TLS 1.2
3. **Monitor connection metrics** - Track handshake times

### Operations

1. **Automate certificate renewal** - Use Let's Encrypt or similar
2. **Monitor certificate expiration** - Use the built-in [certificate inventory](#certificate-inventory-and-rotation) and its warning and critical thresholds
3. **Backup certificates** - Secure storage and recovery
4. **Document configurations** - Maintain deployment records
5. **Test disaster recovery** - Verify certificate restoration

### Development

1. **Use separate certificates** - Development vs production
2. **Self-signed certificates** - For local development
3. **Certificate validation bypass** - Only in development
4. **Configuration management** - Version control settings

## Advanced Configuration

### Custom HTTP Timeouts

```yaml
broker:
  read_timeout: 90    # HTTP read timeout (seconds)
  write_timeout: 90   # HTTP write timeout (seconds)
  idle_timeout: 300   # HTTP idle timeout (seconds)
  
  tls:
    enabled: true
    # ... TLS settings
```

### Multiple Domains

For certificates with multiple domains (SAN certificates):

```yaml
tls:
  certificate: /path/to/multi-domain-cert.pem
  key: /path/to/multi-domain-key.pem
```

The certificate should include all required domains in the Subject Alternative Name (SAN) field.

### Integration with Service Discovery

When using service discovery or load balancers:

```yaml
# Configure for load balancer health checks
broker:
  port: 8080        # Health check port (HTTP)
  
  tls:
    enabled: true
    port: 8443      # Application port (HTTPS)
```

This allows health checks via HTTP while serving application traffic via HTTPS.

---

For additional help or questions about TLS configuration, refer to the Blacksmith documentation or open an issue in the project repository.
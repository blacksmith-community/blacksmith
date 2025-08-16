# Blacksmith TLS Configuration

Blacksmith provides native TLS/SSL support for secure HTTPS communication, eliminating the need for external reverse proxies like Nginx. This document explains how to configure and use TLS with Blacksmith.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Configuration](#configuration)
- [Certificate Management](#certificate-management)
- [Security Settings](#security-settings)
- [Examples](#examples)
- [Migration from Nginx](#migration-from-nginx)
- [Troubleshooting](#troubleshooting)
- [Best Practices](#best-practices)

## Overview

Blacksmith's native TLS implementation provides:

- **Native Go TLS termination** - No external dependencies
- **Dual server mode** - HTTP and HTTPS servers running simultaneously
- **Automatic redirects** - HTTP to HTTPS redirection when TLS is enabled
- **Configurable security** - TLS protocols, cipher suites, and timeouts
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

### Key Components

- **HTTP Server**: Handles normal traffic (TLS disabled) or redirects (TLS enabled)
- **HTTPS Server**: Handles TLS-encrypted traffic when TLS is enabled
- **TLS Configuration**: Manages protocols, cipher suites, and certificates
- **Certificate Validation**: Validates certificate/key pairs on startup

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
    reuse_after: 2
```

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | boolean | `false` | Enable/disable TLS support |
| `port` | string | `"443"` | HTTPS port to listen on |
| `certificate` | string | - | Path to PEM-encoded certificate file |
| `key` | string | - | Path to PEM-encoded private key file |
| `protocols` | string | `"TLSv1.2 TLSv1.3"` | Space-separated list of TLS protocols |
| `ciphers` | string | See [Security Settings](#security-settings) | Colon-separated list of cipher suites |
| `reuse_after` | integer | `2` | TLS session timeout in hours |

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

Blacksmith validates certificates on startup:

- **File existence**: Certificate and key files must exist
- **File format**: Must be valid PEM-encoded files
- **Key matching**: Private key must match the certificate
- **Certificate validity**: Certificate must not be expired

If validation fails, Blacksmith will exit with an error message.

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

Default secure cipher suites:
```
ECDHE-RSA-AES128-SHA256:AES128-GCM-SHA256:HIGH:!MD5:!aNULL:!EDH
```

Common cipher suite configurations:

```yaml
# High security (TLS 1.3 + secure TLS 1.2)
ciphers: "TLS_AES_256_GCM_SHA384:TLS_AES_128_GCM_SHA256:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-RSA-AES128-GCM-SHA256"

# Balanced security and compatibility
ciphers: "ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-RSA-AES128-SHA256:AES128-GCM-SHA256"

# Maximum compatibility (includes legacy ciphers)
ciphers: "HIGH:!aNULL:!eNULL:!EXPORT:!DES:!RC4:!MD5:!PSK:!SRP:!CAMELLIA"
```

**Security Features**:
- Server cipher preference enabled
- Perfect Forward Secrecy (PFS) with ECDHE
- Strong authentication and encryption
- Protection against common attacks

### Session Management

Configure TLS session reuse for performance:

```yaml
tls:
  reuse_after: 2  # Session timeout in hours
```

- **Benefits**: Reduces handshake overhead for returning clients
- **Security**: Sessions expire automatically after the timeout
- **Performance**: Improves connection speed for frequent clients

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
    ciphers: "TLS_AES_256_GCM_SHA384:TLS_CHACHA20_POLY1305_SHA256:TLS_AES_128_GCM_SHA256"
    reuse_after: 1
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
    reuse_after: 24  # Longer timeout for development
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

1. **Use strong cipher suites** - Prefer ECDHE and AES-GCM
2. **Disable weak protocols** - Avoid TLS 1.0 and 1.1
3. **Regular certificate updates** - Monitor expiration dates
4. **Secure key storage** - Protect private key files
5. **Test configurations** - Validate with SSL Labs or similar tools

### Performance

1. **Enable session reuse** - Configure appropriate timeout
2. **Use efficient cipher suites** - AES-GCM and ChaCha20-Poly1305
3. **Consider TLS 1.3** - Better performance than TLS 1.2
4. **Monitor connection metrics** - Track handshake times

### Operations

1. **Automate certificate renewal** - Use Let's Encrypt or similar
2. **Monitor certificate expiration** - Set up alerts
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
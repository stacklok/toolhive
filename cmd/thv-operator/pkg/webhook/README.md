# Webhook Package

This package provides certificate generation and management for Kubernetes admission webhooks in the ToolHive operator.

## Overview

The webhook package handles the generation of self-signed TLS certificates required for the webhook server to communicate securely with the Kubernetes API server.

## Components

### Certificate Generation (`certgen.go`)

The `CertGenerator` type handles the creation of self-signed X.509 certificates:

```go
gen := webhook.NewCertGenerator("webhook-service", "operator-namespace")
caBundle, err := gen.EnsureCertificates()
if err != nil {
    // Handle error
}
```

**Features**:
- Generates RSA 2048-bit private keys
- Creates self-signed certificates valid for 1 year
- Includes multiple DNS names for service discovery
- Writes certificates to `/tmp/k8s-webhook-server/serving-certs/`
- Reuses existing certificates if they exist

**Certificate Properties**:
- Subject CN: `<service-name>.<namespace>.svc`
- DNS names:
  - `<service-name>`
  - `<service-name>.<namespace>`
  - `<service-name>.<namespace>.svc`
  - `<service-name>.<namespace>.svc.cluster.local`
- Key usage: KeyEncipherment, DigitalSignature
- Extended key usage: ServerAuth
- Self-signed (IsCA: true)

### Webhook Setup (`setup.go`)

The `Setup` function integrates certificate generation with the operator lifecycle:

```go
cfg := webhook.GetSetupConfigFromEnv()
err := webhook.Setup(ctx, cfg, k8sClient)
if err != nil {
    // Handle error
}
```

**Features**:
- Reads configuration from environment variables
- Generates or reuses certificates
- Injects CA bundle into ValidatingWebhookConfiguration
- Gracefully handles missing webhook configuration (during initial deployment)

## Usage

### Basic Usage

```go
import webhookpkg "github.com/stacklok/toolhive/cmd/thv-operator/pkg/webhook"

// In main.go before starting the manager
webhookConfig := webhookpkg.GetSetupConfigFromEnv()
if err := webhookpkg.Setup(context.Background(), webhookConfig, nil); err != nil {
    log.Error(err, "unable to setup webhook certificates")
    os.Exit(1)
}
```

### Environment Variables

The package reads the following environment variables:

- `POD_NAMESPACE`: Namespace where the operator runs (default: `toolhive-operator-system`)
- `WEBHOOK_SERVICE_NAME`: Name of the webhook Service (default: `toolhive-operator-webhook-service`)
- `WEBHOOK_CONFIG_NAME`: Name of the ValidatingWebhookConfiguration (default: `toolhive-operator-validating-webhook-configuration`)
- `ENABLE_WEBHOOKS`: Whether webhooks are enabled (default: `true`)
- `ENABLE_VMCP`: Whether Virtual MCP is enabled (webhooks require VMCP, default: `true`)

### Custom Configuration

```go
cfg := webhook.SetupConfig{
    ServiceName:       "custom-webhook-service",
    Namespace:         "custom-namespace",
    WebhookConfigName: "custom-webhook-config",
    Enabled:           true,
}

err := webhook.Setup(ctx, cfg, k8sClient)
```

### Manual Certificate Generation

```go
gen := &webhook.CertGenerator{
    CertDir:     "/custom/cert/dir",
    ServiceName: "my-webhook-service",
    Namespace:   "my-namespace",
}

// Generate new certificates (overwrites existing)
caBundle, err := gen.Generate()

// Or ensure certificates exist (reuse if available)
caBundle, err := gen.EnsureCertificates()

// Check if certificates already exist
if gen.CertificatesExist() {
    // Use existing certificates
}
```

### CA Bundle Injection

```go
// Inject CA bundle into webhook configuration
err := webhook.InjectCABundle(ctx, k8sClient, "webhook-config-name", caBundle)
if err != nil {
    // Handle error (config might not exist yet)
}
```

## File Permissions

The package uses secure file permissions:

- Certificate directory: `0750` (owner: rwx, group: r-x, others: none)
- Private key file: `0600` (owner: rw, others: none)
- Certificate file: `0600` (owner: rw, others: none)

## Testing

The package includes comprehensive unit tests:

```bash
go test ./cmd/thv-operator/pkg/webhook/...
```

**Test Coverage**:
- Certificate generation and validation
- File I/O operations
- Certificate reuse logic
- CA bundle injection
- Environment variable configuration
- Error handling

## Security Considerations

### Certificate Security

- Private keys are never logged or exposed
- Private key files have restrictive permissions (0600)
- Certificates are stored in ephemeral storage (`emptyDir` volume)
- Each pod restart generates new certificates (or reuses existing)

### Validation

The package validates:
- Certificate PEM encoding
- Certificate validity (can be parsed by x509 package)
- Required DNS names are included
- Key usage and extended key usage are correct

### Limitations

- **Self-signed certificates**: Not suitable for production environments requiring CA-signed certificates
- **1-year validity**: Requires manual rotation before expiry
- **No automatic rotation**: Operator restart required for certificate rotation

## Future Enhancements

Potential improvements for this package:

1. **Automatic certificate rotation**: Monitor certificate expiry and rotate before expiration
2. **External CA support**: Support for using external CA for certificate signing
3. **cert-manager integration**: Optional integration with cert-manager for certificate lifecycle management
4. **Certificate metrics**: Export Prometheus metrics for certificate expiry
5. **Configurable validity**: Allow certificate validity period to be configured

## Integration Points

This package integrates with:

- `cmd/thv-operator/main.go`: Called during operator startup
- `deploy/charts/operator/templates/`: Helm charts reference webhook resources
- `cmd/thv-operator/api/v1alpha1/*_webhook.go`: Webhook implementations use the certificates

## Dependencies

External dependencies:
- `k8s.io/api/admissionregistration/v1`: For ValidatingWebhookConfiguration types
- `sigs.k8s.io/controller-runtime/pkg/client`: For Kubernetes client operations
- `sigs.k8s.io/controller-runtime/pkg/log`: For logging

Standard library:
- `crypto/rsa`, `crypto/x509`: Certificate generation
- `encoding/pem`: PEM encoding
- `os`: File I/O operations
- `path/filepath`: Path manipulation

## References

- [Go crypto/x509 Documentation](https://pkg.go.dev/crypto/x509)
- [Kubernetes Admission Webhooks](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)
- [controller-runtime Webhook Server](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/webhook)

# Webhook Integration Tests

This directory contains integration tests for the ToolHive operator admission webhooks.

## Overview

These tests verify that the admission webhooks work correctly in a real Kubernetes environment using `envtest`. The tests validate that:

1. Invalid resources are rejected at admission time
2. Valid resources are accepted
3. Update operations are properly validated
4. Error messages are clear and actionable

## Webhooks Tested

### 1. VirtualMCPServer Webhook

**Endpoint**: `/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpserver`

**Tests**:
- ✅ Rejects VirtualMCPServer without required `groupRef`
- ✅ Accepts valid VirtualMCPServer configuration
- ✅ Rejects invalid backend auth configuration
- ✅ Rejects invalid aggregation strategies
- ✅ Validates update operations

### 2. MCPExternalAuthConfig Webhook

**Endpoint**: `/validate-toolhive-stacklok-dev-v1alpha1-mcpexternalauthconfig`

**Tests**:
- ✅ Rejects mismatched auth type and configuration
- ✅ Accepts valid tokenExchange configuration
- ✅ Accepts valid bearerToken configuration
- ✅ Rejects multiple conflicting auth configs
- ✅ Rejects invalid upstream provider configurations
- ✅ Validates update operations

### 3. VirtualMCPCompositeToolDefinition Webhook

**Endpoint**: `/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpcompositetooldefinition`

**Tests**:
- ✅ Rejects composite tools without required fields (name, description)
- ✅ Accepts valid composite tool definitions
- ✅ Rejects invalid step configurations
- ✅ Rejects duplicate step IDs
- ✅ Rejects invalid error handling configurations

## Running the Tests

### Prerequisites

- Go 1.23 or later
- `envtest` binaries (automatically downloaded if not present)

### Run Tests

```bash
# From the repository root
task test-integration-webhooks

# Or directly with go test
go test -v ./cmd/thv-operator/test-integration/webhooks/...

# Run specific test
go test -v ./cmd/thv-operator/test-integration/webhooks/ -ginkgo.focus="VirtualMCPServer Webhook"
```

### Test Output

```
Running Suite: Webhook Integration Test Suite
==============================================
Random Seed: 1234567890

Will run 15 of 15 specs
•••••••••••••••

Ran 15 of 15 Specs in 2.345 seconds
SUCCESS! -- 15 Passed | 0 Failed | 0 Pending | 0 Skipped
```

## Test Architecture

### Suite Setup (`suite_test.go`)

1. **envtest Environment**: Creates a local Kubernetes API server with webhook support
2. **CRD Installation**: Loads all operator CRDs from `deploy/charts/operator-crds/files/crds/`
3. **Webhook Configuration**: Loads webhook configuration from `config/webhook/`
4. **Manager Setup**: Starts a controller-runtime manager with webhook server
5. **Webhook Registration**: Registers all three webhooks with the manager

### Test Structure

Tests follow the Ginkgo BDD pattern:

```go
Context("VirtualMCPServer Webhook", func() {
    It("should reject invalid resource", func() {
        // Create invalid resource
        // Expect error with specific message
    })

    It("should accept valid resource", func() {
        // Create valid resource
        // Expect no error
        // Clean up
    })
})
```

## Key Features

### 1. Real Kubernetes API Server

Tests use `envtest` which starts a real Kubernetes API server (etcd + apiserver) to ensure webhooks work exactly as they would in production.

### 2. Webhook TLS

`envtest` automatically generates TLS certificates for the webhook server and configures the API server to trust them.

### 3. Parallel Execution

Tests are marked with `t.Parallel()` for faster execution where safe.

### 4. Clean Test Isolation

Each test creates its own resources in the `default` namespace and cleans up after itself.

## Debugging

### Enable Verbose Output

```bash
go test -v ./cmd/thv-operator/test-integration/webhooks/ -ginkgo.v
```

### See Webhook Requests

Set log level to Debug in `suite_test.go`:

```go
logLevel := zapcore.DebugLevel  // Changed from ErrorLevel
```

### Check Webhook Server

If tests fail with connection errors:

```bash
# Check if envtest binaries are installed
ls $(go env GOPATH)/bin/envtest*

# Download envtest binaries if missing
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
setup-envtest use
```

## Common Issues

### Port Conflicts

If tests fail with "address already in use":

```bash
# Kill any running envtest processes
pkill -9 etcd
pkill -9 kube-apiserver
```

### CRD Not Found

If tests fail with "no matches for kind":

```bash
# Verify CRDs exist
ls deploy/charts/operator-crds/files/crds/

# Regenerate CRDs if needed
task operator-manifests
```

### Webhook Not Called

If validation doesn't work:

1. Check webhook configuration is loaded
2. Verify manager started successfully
3. Ensure webhook server is listening (check logs)

## Adding New Tests

To add tests for new webhooks:

1. Register webhook in `suite_test.go`:
   ```go
   err = (&mcpv1alpha1.NewResource{}).SetupWebhookWithManager(k8sManager)
   Expect(err).ToNot(HaveOccurred())
   ```

2. Add test cases in `webhook_validation_test.go`:
   ```go
   Context("NewResource Webhook", func() {
       It("should reject invalid resource", func() {
           // Test implementation
       })
   })
   ```

3. Run tests to verify

## Integration with CI

These tests should be run in CI to ensure:

- Webhooks are properly configured
- Validation logic catches invalid resources
- No regressions in webhook behavior

Add to CI pipeline:

```yaml
- name: Run webhook integration tests
  run: go test -v ./cmd/thv-operator/test-integration/webhooks/...
```

## Related Documentation

- [Webhook Implementation](../../pkg/webhook/README.md)
- [Operator Webhooks Guide](../../docs/webhooks.md)
- [Unit Tests for Validation Logic](../../api/v1alpha1/*_webhook_test.go)

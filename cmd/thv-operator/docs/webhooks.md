# Admission Webhooks

The ToolHive operator implements admission webhooks to validate Custom Resource Definitions (CRDs) before they are persisted to the Kubernetes API server. This document describes the webhook implementation and configuration.

## Overview

The operator implements ValidatingWebhooks for the following CRDs:

1. **VirtualMCPServer**: Validates vMCP server configuration including authentication, aggregation, and composite tools
2. **VirtualMCPCompositeToolDefinition**: Validates composite tool workflow definitions
3. **MCPExternalAuthConfig**: Validates external authentication configuration (OAuth/OIDC)

## Architecture

### Certificate Management

The operator uses self-signed certificates for webhook TLS communication:

- Certificates are automatically generated at operator startup
- Generated certificates are stored in `/tmp/k8s-webhook-server/serving-certs/`
- Certificate validity: 1 year
- DNS names included in certificate:
  - `<service-name>`
  - `<service-name>.<namespace>`
  - `<service-name>.<namespace>.svc`
  - `<service-name>.<namespace>.svc.cluster.local`

### Components

**Certificate Generation Package** (`cmd/thv-operator/pkg/webhook/`):
- `certgen.go`: Generates self-signed X.509 certificates
- `setup.go`: Integrates certificate generation with operator startup

**Main Integration** (`cmd/thv-operator/main.go`):
- Calls webhook setup before starting the manager
- Generates certificates if they don't exist
- Injects CA bundle into ValidatingWebhookConfiguration

**Helm Chart Resources** (`deploy/charts/operator/templates/`):
- `webhook-service.yaml`: Service exposing port 9443 for webhook server
- `validating-webhook-configuration.yaml`: ValidatingWebhookConfiguration resource
- `webhook-clusterrole.yaml`: RBAC permissions for webhook configuration updates
- `webhook-clusterrolebinding.yaml`: Binds webhook role to operator service account

## Configuration

### Enabling/Disabling Webhooks

Webhooks are controlled by the Helm chart value `operator.webhook.enabled`:

```yaml
operator:
  webhook:
    enabled: true  # Set to false to disable webhooks
    failurePolicy: Fail  # Can be "Fail" or "Ignore"
```

When `enabled: false`:
- Webhook Service is not created
- ValidatingWebhookConfiguration is not created
- Webhook RBAC resources are not created
- Certificate generation is skipped
- Webhook server is not started

**Note**: Webhooks require the Virtual MCP feature to be enabled (`operator.features.virtualMCP: true`).

### Environment Variables

The following environment variables are automatically set by the Helm chart when webhooks are enabled:

- `ENABLE_WEBHOOKS`: Set to "true" when webhooks are enabled
- `WEBHOOK_SERVICE_NAME`: Name of the webhook Service (default: `toolhive-operator-webhook-service`)
- `WEBHOOK_CONFIG_NAME`: Name of the ValidatingWebhookConfiguration (default: `toolhive-operator-validating-webhook-configuration`)
- `POD_NAMESPACE`: Namespace where the operator runs

### Deployment Updates

When webhooks are enabled, the operator Deployment is configured with:

**Ports**:
- Container port 9443 exposed for webhook server

**Volumes**:
- EmptyDir volume mounted at `/tmp/k8s-webhook-server/serving-certs` for certificate storage

**Security**:
- Volume is writable (certificates are generated at runtime)
- Volume is ephemeral (certificates regenerated on pod restart)

## Webhook Endpoints

The webhook server exposes the following endpoints:

### VirtualMCPServer Validation

**Path**: `/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpserver`

Validates:
- `spec.config.groupRef` is set (required)
- Incoming authentication configuration
- Outgoing authentication configuration and backend references
- Aggregation conflict resolution strategies
- Composite tool definitions

### VirtualMCPCompositeToolDefinition Validation

**Path**: `/validate-toolhive-stacklok-dev-v1alpha1-virtualmcpcompositetooldefinition`

Validates composite tool workflow configurations.

### MCPExternalAuthConfig Validation

**Path**: `/validate-toolhive-stacklok-dev-v1alpha1-mcpexternalauthconfig`

Validates external OAuth/OIDC authentication configuration.

## Failure Policy

The `failurePolicy` setting controls what happens when the webhook server is unavailable:

- `Fail` (default): Blocks resource creation/updates if webhook is unavailable
- `Ignore`: Allows resource creation/updates even if webhook is unavailable

**Recommendation**: Use `Fail` in production to ensure validation is always performed.

## Certificate Lifecycle

### Initial Deployment

1. Operator pod starts
2. Certificate generation runs before manager starts
3. Certificates are generated and written to `/tmp/k8s-webhook-server/serving-certs/`
4. CA bundle is injected into ValidatingWebhookConfiguration (if it exists)
5. Webhook server starts using the generated certificates
6. Kubernetes API server validates webhook TLS using the CA bundle

### Pod Restart

1. New pod starts
2. Certificate generation detects existing certificates (or generates new ones)
3. CA bundle is re-injected into ValidatingWebhookConfiguration
4. Webhook server starts

### Certificate Rotation

Currently, certificates are valid for 1 year. To rotate certificates:

1. Delete the operator pod
2. New pod will generate fresh certificates
3. CA bundle will be automatically updated

**Future Enhancement**: Implement automatic certificate rotation before expiry.

## RBAC Permissions

The operator requires the following RBAC permissions for webhook functionality:

```yaml
apiGroups:
  - admissionregistration.k8s.io
resources:
  - validatingwebhookconfigurations
verbs:
  - get
  - list
  - watch
  - update
  - patch
```

These permissions allow the operator to inject the CA bundle into the ValidatingWebhookConfiguration at startup.

## Troubleshooting

### Webhook Server Not Starting

**Symptoms**: Operator pod fails to start with certificate errors

**Causes**:
- Certificate directory `/tmp/k8s-webhook-server/serving-certs` is not writable
- Volume mount is missing from pod spec

**Solutions**:
1. Check pod events: `kubectl describe pod -n toolhive-operator-system <pod-name>`
2. Verify volume mount: `kubectl get pod -n toolhive-operator-system <pod-name> -o yaml | grep -A 5 webhook-certs`
3. Check operator logs: `kubectl logs -n toolhive-operator-system <pod-name>`

### Validation Failures

**Symptoms**: Resources fail to create with webhook validation errors

**Causes**:
- Invalid resource specification
- Webhook validation logic is too strict

**Solutions**:
1. Check the error message for specific validation failures
2. Review the resource spec against the validation rules
3. Check webhook logs for detailed validation errors

### Webhook Unavailable

**Symptoms**: Resources fail to create with "webhook unavailable" errors

**Causes**:
- Operator pod is not running
- Webhook Service is not routing to operator pod
- Certificate is invalid or expired

**Solutions**:
1. Check operator pod status: `kubectl get pods -n toolhive-operator-system`
2. Verify Service endpoints: `kubectl get endpoints -n toolhive-operator-system`
3. Check certificate validity in operator logs
4. Verify ValidatingWebhookConfiguration has correct caBundle: `kubectl get validatingwebhookconfigurations`

### CA Bundle Not Injected

**Symptoms**: Webhook fails with TLS handshake errors

**Causes**:
- Operator doesn't have RBAC permissions to update ValidatingWebhookConfiguration
- ValidatingWebhookConfiguration doesn't exist when operator starts

**Solutions**:
1. Check operator logs for CA bundle injection errors
2. Verify RBAC permissions: `kubectl auth can-i update validatingwebhookconfigurations --as=system:serviceaccount:toolhive-operator-system:toolhive-operator`
3. Manually inject CA bundle if needed (see below)

### Manual CA Bundle Injection

If automatic CA bundle injection fails, you can manually inject it:

```bash
# Extract CA bundle from operator pod
kubectl exec -n toolhive-operator-system <pod-name> -- cat /tmp/k8s-webhook-server/serving-certs/tls.crt | base64 | tr -d '\n'

# Update ValidatingWebhookConfiguration
kubectl patch validatingwebhookconfiguration toolhive-operator-validating-webhook-configuration \
  --type='json' \
  -p='[{"op": "replace", "path": "/webhooks/0/clientConfig/caBundle", "value": "<base64-ca-bundle>"}]'
```

## Development

### Testing Webhooks Locally

To test webhook validation locally:

1. Deploy operator with webhooks enabled
2. Create test resources and verify validation

Example:

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: VirtualMCPServer
metadata:
  name: test-vmcp
spec:
  config:
    # Missing required field: groupRef
    aggregation:
      conflictResolution: invalid  # Invalid value
```

This should fail validation with appropriate error messages.

### Adding New Webhooks

To add validation for a new CRD:

1. Implement webhook interface in `cmd/thv-operator/api/v1alpha1/<crd>_webhook.go`:
   ```go
   func (r *MyCRD) SetupWebhookWithManager(mgr ctrl.Manager) error
   func (r *MyCRD) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error)
   func (r *MyCRD) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error)
   ```

2. Register webhook in `cmd/thv-operator/main.go` (in appropriate feature setup function)

3. Add webhook configuration to `deploy/charts/operator/templates/validating-webhook-configuration.yaml`

4. Add kubebuilder marker to CRD:
   ```go
   // +kubebuilder:webhook:path=/validate-...,mutating=false,failurePolicy=fail,...
   ```

5. Run `task operator-manifests` to update generated webhook configuration

## References

- [Kubernetes Admission Webhooks](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)
- [controller-runtime Webhook Guide](https://book.kubebuilder.io/cronjob-tutorial/webhook-implementation.html)
- [Certificate Management in Kubernetes](https://kubernetes.io/docs/tasks/tls/managing-tls-in-a-cluster/)

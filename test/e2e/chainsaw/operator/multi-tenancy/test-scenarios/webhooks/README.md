# Webhook Validation E2E Tests

This directory contains end-to-end tests for the ToolHive operator admission webhooks using the Chainsaw testing framework.

## What These Tests Cover

The tests validate that the admission webhooks correctly reject invalid configurations:

1. **VirtualMCPServer** - Rejects resources without required `groupRef`
2. **MCPExternalAuthConfig** - Rejects resources with conflicting authentication types (both oauth2 and oidc)

## Test Structure

- `chainsaw-test.yaml` - Main test definition with 2 steps:
  1. Check prerequisites (CRDs and webhook configuration exist)
  2. Test that invalid configurations are rejected by webhooks

## Running the Tests

### Prerequisites

1. Kubernetes cluster (Kind recommended)
2. ToolHive operator deployed with webhooks enabled
3. Chainsaw CLI installed

### Run All E2E Tests (Including Webhooks)

From the repository root:

```bash
task thv-operator-e2e-test
```

### Run Only Webhook Tests

From the repository root with Chainsaw installed:

```bash
# Set up cluster and deploy operator
kind create cluster --name toolhive --config test/e2e/thv-operator/kind-config.yaml
task operator-install-crds

# Build and deploy operator with webhooks enabled
helm upgrade --install toolhive-operator deploy/charts/operator \
  --set operator.webhook.enabled=true \
  --namespace toolhive-system \
  --create-namespace

# Run webhook tests
chainsaw test \
  --test-dir test/e2e/chainsaw/operator/multi-tenancy/test-scenarios/webhooks
```

## Expected Behavior

### Invalid Resources

When attempting to create invalid resources, the webhooks should:
- **Reject** the request
- Return an error message containing "denied the request"
- Provide validation error details

Example error for invalid VirtualMCPServer:
```
Error from server (Forbidden): admission webhook "virtualmcpserver.mcp.stacklok.com" denied the request: groupRef is required
```

### Valid Resources

When creating valid resources, the webhooks should:
- **Accept** the request
- Allow the resource to be created
- Resources appear in `kubectl get` commands

## Troubleshooting

### Webhooks Not Rejecting Invalid Resources

Check if webhooks are enabled:
```bash
kubectl get validatingwebhookconfiguration
kubectl describe validatingwebhookconfiguration toolhive-operator-validating-webhook-configuration
```

Verify the operator has webhook certificates:
```bash
kubectl logs -n toolhive-system deployment/toolhive-operator | grep webhook
```

### Webhook Connection Errors

Check if the webhook service exists and is accessible:
```bash
kubectl get svc -n toolhive-system | grep webhook
kubectl describe svc -n toolhive-system toolhive-operator-webhook-service
```

Verify the operator pod is running:
```bash
kubectl get pods -n toolhive-system
kubectl logs -n toolhive-system deployment/toolhive-operator
```

## Running in CI

These webhook E2E tests can be run in CI by deploying the operator with webhooks enabled:

```yaml
# In your workflow
helm upgrade --install toolhive-operator deploy/charts/operator \
  --set operator.webhook.enabled=true \
  --namespace toolhive-system \
  --create-namespace

# Wait for webhook service
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=operator -n toolhive-system --timeout=2m

# Run webhook tests
chainsaw test --test-dir test/e2e/chainsaw/operator/multi-tenancy/test-scenarios/webhooks
```

**Note**: These tests require webhooks to be enabled, which means the operator must be deployed with `operator.webhook.enabled=true`.

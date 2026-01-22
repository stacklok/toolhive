# Custom ServiceAccount E2E Test

Tests that users can provide their own ServiceAccount for ToolHive workloads, enabling use cases like:
- Using ServiceAccounts with imagePullSecrets for private registries
- Using ServiceAccounts with custom RBAC permissions
- Integrating with external identity/authentication systems

## Test Coverage

### MCPServer
- ✅ Custom ServiceAccount with imagePullSecrets is used
- ✅ Operator does NOT create its own ServiceAccount/Role/RoleBinding
- ✅ StatefulSet pod uses the custom ServiceAccount

## Real-World Use Case

This validates the solution for users who need to pull images from private registries:

```yaml
# 1. Create ServiceAccount with imagePullSecrets
apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-custom-sa
imagePullSecrets:
  - name: my-registry-secret

# 2. Reference it in the workload
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-server
spec:
  image: custom-registry.example.com/mcp-server:latest
  serviceAccount: my-custom-sa  # ← Uses custom SA with imagePullSecrets
```

## Running the Test

```bash
# Run all single-tenancy tests (includes this one)
task operator-e2e-test-chainsaw

# Run only this test
chainsaw test --test-dir test/e2e/chainsaw/operator/single-tenancy/test-scenarios/custom-service-account
```

## Notes

- The test uses `my-registry-secret` as a placeholder for imagePullSecrets
- The secret itself doesn't need to exist for this test to pass
- The test focuses on verifying the ServiceAccount is correctly used, not on actual image pulling
- The same pattern applies to VirtualMCPServer and MCPRemoteProxy

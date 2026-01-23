# EmbeddingServer Multi-Tenancy E2E Tests

This directory contains end-to-end tests for the EmbeddingServer CRD in multi-tenancy mode.

## Test Scenario

### Multi-Tenancy EmbeddingServer

Tests EmbeddingServer deployment across multiple namespaces to verify isolation.

**Coverage:**
- Namespace creation for testing
- EmbeddingServer deployment in multiple namespaces
- Resource isolation verification
- Service network isolation
- Independent endpoint testing

**Resources tested:**
- Two test namespaces (`toolhive-test-ns-1`, `toolhive-test-ns-2`)
- EmbeddingServer CRs in each namespace
- Separate StatefulSets per namespace
- Separate ClusterIP Services per namespace
- Network isolation between namespaces

**Verification:**
1. EmbeddingServers exist in both namespaces
2. StatefulSets are created in correct namespaces
3. Services have different ClusterIPs
4. Health endpoints respond in both namespaces
5. No cross-namespace interference

**Command:**
```bash
chainsaw test --test-dir test/e2e/chainsaw/operator/multi-tenancy/test-scenarios/embeddingserver
```

## Test Flow

1. **Setup:**
   - Verify operator is ready
   - Create test namespace 1 (`toolhive-test-ns-1`)
   - Create test namespace 2 (`toolhive-test-ns-2`)

2. **Deploy EmbeddingServer in Namespace 1:**
   - Apply EmbeddingServer CR
   - Assert CR is created
   - Assert status is "Running"
   - Assert StatefulSet is ready
   - Assert Service is created

3. **Deploy EmbeddingServer in Namespace 2:**
   - Apply EmbeddingServer CR
   - Assert CR is created
   - Assert status is "Running"
   - Assert StatefulSet is ready
   - Assert Service is created

4. **Verify Isolation:**
   - Check EmbeddingServers exist in correct namespaces
   - Verify StatefulSets are in separate namespaces
   - Verify Services have different ClusterIPs
   - Confirm no resource leakage between namespaces

5. **Test Endpoints:**
   - Test health endpoint in namespace 1
   - Test health endpoint in namespace 2
   - Verify both respond independently

## Configuration Differences

Each namespace deployment includes a `NAMESPACE_IDENTIFIER` environment variable to distinguish instances:

**Namespace 1:**
```yaml
env:
  - name: NAMESPACE_IDENTIFIER
    value: "namespace-1"
```

**Namespace 2:**
```yaml
env:
  - name: NAMESPACE_IDENTIFIER
    value: "namespace-2"
```

## Expected Behavior

In multi-tenancy mode, the operator should:

1. **Namespace Isolation:**
   - Each EmbeddingServer operates independently
   - Resources are scoped to their namespace
   - No shared state between namespaces

2. **Resource Naming:**
   - Same resource names can exist in different namespaces
   - StatefulSet: `embedding-<name>`
   - Service: `embedding-<name>`

3. **Network Isolation:**
   - Each Service gets a unique ClusterIP
   - Services are only accessible within their namespace (by default)
   - No network interference between instances

4. **Independent Lifecycle:**
   - Updates to one namespace don't affect the other
   - Deletion in one namespace doesn't cascade to the other

## Prerequisites

- Kubernetes cluster with multi-tenancy support
- ToolHive operator installed with multi-namespace support
- Chainsaw test framework installed
- Sufficient cluster resources for multiple embedding instances

## Cleanup

Chainsaw automatically cleans up test resources including:
- EmbeddingServer CRs
- StatefulSets
- Services
- Test namespaces

## Troubleshooting

If multi-tenancy tests fail, check:

1. Operator namespace scope:
   ```bash
   kubectl get deployment -n toolhive-system toolhive-operator-controller-manager -o yaml | grep -A 5 WATCH_NAMESPACE
   ```

2. RBAC permissions for both namespaces:
   ```bash
   kubectl get rolebinding -n toolhive-test-ns-1
   kubectl get rolebinding -n toolhive-test-ns-2
   ```

3. EmbeddingServer status in each namespace:
   ```bash
   kubectl get embeddingserver -n toolhive-test-ns-1
   kubectl get embeddingserver -n toolhive-test-ns-2
   ```

4. Network policies (if any):
   ```bash
   kubectl get networkpolicy -n toolhive-test-ns-1
   kubectl get networkpolicy -n toolhive-test-ns-2
   ```

## Notes

- Tests use the same model across namespaces for consistency
- Each instance is lightweight (CPU-based) for faster testing
- Services are ClusterIP type (not exposed externally)
- Test namespaces are ephemeral and cleaned up after tests

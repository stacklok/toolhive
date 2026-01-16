# EmbeddingServer E2E Tests

This directory contains end-to-end tests for the EmbeddingServer CRD in single-tenancy mode.

## Test Scenarios

### 1. Basic EmbeddingServer (`basic/`)

Tests basic EmbeddingServer deployment without model caching.

**Coverage:**
- EmbeddingServer resource creation
- Deployment creation and readiness
- Service creation with ClusterIP
- Health endpoint verification

**Resources tested:**
- EmbeddingServer CR with minimal configuration
- Deployment with single replica
- ClusterIP Service on port 8080

**Command:**
```bash
chainsaw test --test-dir test/e2e/chainsaw/operator/single-tenancy/test-scenarios/embeddingserver/basic
```

### 2. EmbeddingServer with Model Cache (`with-cache/`)

Tests EmbeddingServer deployment with persistent model caching enabled.

**Coverage:**
- EmbeddingServer with ModelCache configuration
- PersistentVolumeClaim creation and binding
- Volume mount verification in deployment
- Model cache persistence across pod restarts

**Resources tested:**
- EmbeddingServer CR with ModelCache enabled
- PersistentVolumeClaim (5Gi, ReadWriteOnce)
- Deployment with mounted cache volume
- ClusterIP Service

**Command:**
```bash
chainsaw test --test-dir test/e2e/chainsaw/operator/single-tenancy/test-scenarios/embeddingserver/with-cache
```

### 3. EmbeddingServer Lifecycle (`lifecycle/`)

Tests complete lifecycle operations for EmbeddingServer.

**Coverage:**
- Create initial EmbeddingServer
- Scale replicas (1 → 2)
- Update environment variables
- Verify updates propagate to Deployment
- Delete EmbeddingServer
- Verify resource cleanup

**Resources tested:**
- EmbeddingServer CR updates
- Deployment scaling
- Environment variable propagation
- Resource deletion and cleanup

**Command:**
```bash
chainsaw test --test-dir test/e2e/chainsaw/operator/single-tenancy/test-scenarios/embeddingserver/lifecycle
```

## Running All Tests

To run all EmbeddingServer single-tenancy tests:

```bash
chainsaw test --test-dir test/e2e/chainsaw/operator/single-tenancy/test-scenarios/embeddingserver
```

## Test Configuration

All tests use the following common settings:

- **Model:** `sentence-transformers/all-MiniLM-L6-v2` (lightweight for testing)
- **Image:** `ghcr.io/huggingface/text-embeddings-inference:cpu-1.5`
- **Namespace:** `toolhive-system`
- **Port:** 8080
- **Resource Limits:**
  - CPU: 500m
  - Memory: 512Mi
- **Resource Requests:**
  - CPU: 250m
  - Memory: 256Mi

## Test Assertions

Each test verifies:

1. **EmbeddingServer Status:**
   - Phase: "Running"
   - ReadyReplicas matches expected count
   - URL is set (when applicable)

2. **Deployment:**
   - AvailableReplicas matches expected count
   - ReadyReplicas matches expected count
   - Proper labels and selectors

3. **Service:**
   - Type: ClusterIP
   - Port: 8080
   - TargetPort: 8080

4. **PVC (when applicable):**
   - Status: Bound
   - Size: As specified
   - AccessMode: As specified
   - Mounted in deployment

## Prerequisites

- Kubernetes cluster with ToolHive operator installed
- Chainsaw test framework installed
- Storage provisioner (for cache tests)
- Sufficient cluster resources for running embedding models

## Troubleshooting

If tests fail, check:

1. Operator logs:
   ```bash
   kubectl logs -n toolhive-system -l control-plane=controller-manager
   ```

2. EmbeddingServer status:
   ```bash
   kubectl describe embeddingserver <name> -n toolhive-system
   ```

3. Deployment status:
   ```bash
   kubectl describe deployment embedding-<name> -n toolhive-system
   ```

4. Pod logs:
   ```bash
   kubectl logs -n toolhive-system -l app.kubernetes.io/name=mcpembedding
   ```

## Notes

- Tests use CPU-based image to avoid GPU requirements
- Model downloads may take time on first run
- Tests include health endpoint verification via curl
- Cleanup is automatic via Chainsaw framework

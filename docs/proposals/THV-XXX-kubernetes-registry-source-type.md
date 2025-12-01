# Proposal: Kubernetes Source Type for Registry Server

## Status

**Proposed** - Supercedes [toolhive#2591](https://github.com/stacklok/toolhive/pull/2591)

## Summary

Add a native `kubernetes` source type to the ToolHive Registry Server that directly watches Kubernetes resources (MCPServer, MCPRemoteProxy, VirtualMCPServer) and builds registry entries from annotated resources. This eliminates the need for an intermediate ConfigMap-based approach.

## Motivation

### Problem Statement

We want to automatically populate the MCP registry with servers deployed in Kubernetes. The previous proposal ([toolhive#2591](https://github.com/stacklok/toolhive/pull/2591)) suggested having the ToolHive operator:

1. Watch annotated MCP resources and HTTPRoutes
2. Aggregate discovered servers into per-namespace ConfigMaps
3. Have the registry server read those ConfigMaps

This approach has several drawbacks:

- **ConfigMap size limits**: Kubernetes ConfigMaps are limited to 1MB, constraining scalability
- **Backup complexity**: ConfigMaps as intermediate artifacts complicate backup/restore workflows
- **Two-hop latency**: Changes must propagate through operator → ConfigMap → registry server
- **Split logic**: Registry population logic is split across two components

### Proposed Solution

Add a `kubernetes` source type to the registry server that directly queries Kubernetes resources using the same sync patterns as existing sources (git, api, file). The registry server already uses `controller-runtime` and has a clean provider abstraction that fits this model naturally.

## Design

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                    Registry API Server                          │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                 KubernetesRegistryHandler                 │  │
│  │                                                           │  │
│  │  1. List MCPServer, MCPRemoteProxy, VirtualMCPServer     │  │
│  │  2. Filter by namespace/labels + require annotations     │  │
│  │  3. Build UpstreamRegistry entries from annotations      │  │
│  │  4. Return FetchResult (same as git/api/file handlers)   │  │
│  │                                                           │  │
│  └───────────────────────────────────────────────────────────┘  │
│                              │                                   │
│                              ▼                                   │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  SyncManager + StorageManager (existing infrastructure)  │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### Configuration

```yaml
registryName: "toolhive-cluster"

registries:
  - name: "cluster-mcp-servers"
    format: upstream
    kubernetes:
      # Namespace filtering (empty = all namespaces)
      namespaces: []

      # Optional label selector (standard k8s selector syntax)
      labelSelector: ""

    syncPolicy:
      interval: "30s"

auth:
  mode: oauth
  # ... standard auth config
```

### Annotations

MCP resources use annotations under the `toolhive.stacklok.dev` prefix to control registry export. A resource is only included in the registry if it has the required annotations.

| Annotation | Required | Description |
|------------|----------|-------------|
| `toolhive.stacklok.dev/registry-export` | Yes | Must be `"true"` to include in registry |
| `toolhive.stacklok.dev/registry-url` | Yes | The external endpoint URL for this server |
| `toolhive.stacklok.dev/registry-description` | No | Override the description in registry |
| `toolhive.stacklok.dev/registry-tier` | No | Server tier classification |

Resources without `registry-export: "true"` are ignored. Resources with `registry-export: "true"` but missing `registry-url` are logged as warnings and skipped.

### Example MCPServer

```yaml
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-mcp-server
  namespace: production
  annotations:
    toolhive.stacklok.dev/registry-export: "true"
    toolhive.stacklok.dev/registry-url: "https://mcp.example.com/servers/my-mcp-server"
    toolhive.stacklok.dev/registry-description: "Production MCP server for code analysis"
spec:
  # ... MCP server spec
```

### Handler Implementation

The `KubernetesRegistryHandler` implements the existing `RegistryHandler` interface:

```go
type RegistryHandler interface {
    FetchRegistry(ctx context.Context, regCfg *config.RegistryConfig) (*FetchResult, error)
    Validate(regCfg *config.RegistryConfig) error
    CurrentHash(ctx context.Context, regCfg *config.RegistryConfig) (string, error)
}
```

Implementation:

1. **FetchRegistry**: Lists MCP resources, filters to those with `registry-export: "true"`, builds `UpstreamRegistry` entries from annotations
2. **CurrentHash**: Same list/filter, computes hash for change detection
3. **Validate**: Validates kubernetes config (label selector syntax, etc.)

### Sync Behavior

Uses the existing `SyncManager` infrastructure:

1. Every `syncPolicy.interval`, check if sync is needed via `CurrentHash()`
2. If hash changed, call `FetchRegistry()` to get full data
3. Store result via `StorageManager` (file or database)

This is identical to how git, api, and file sources work today.

### RBAC Requirements

The registry server's ServiceAccount needs read access to MCP resources:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: toolhive-registry-reader
rules:
  - apiGroups: ["toolhive.stacklok.dev"]
    resources: ["mcpservers", "mcpremoteproxies", "virtualmcpservers"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: toolhive-registry-reader
subjects:
  - kind: ServiceAccount
    name: toolhive-registry-api
    namespace: toolhive-system
roleRef:
  kind: ClusterRole
  name: toolhive-registry-reader
  apiGroup: rbac.authorization.k8s.io
```

For namespace-scoped deployments, use Role/RoleBinding instead.

## Alternatives Considered

### ConfigMap-based approach (toolhive#2591)

The original proposal had the operator write to ConfigMaps, with the registry server reading them.

**Rejected due to the following concerns:**

#### ConfigMap size limits

Kubernetes ConfigMaps are hard-limited to 1MB. While individual MCP server entries are small, a cluster with many servers across many namespaces could approach this limit. The per-namespace ConfigMap approach in the original proposal mitigates this somewhat, but introduces its own complexity (multiple ConfigMaps to aggregate) and still imposes a ceiling on servers-per-namespace.

#### Backup and restore complexity

ConfigMaps as intermediate artifacts create operational challenges:

- **Backup ambiguity**: Should ConfigMaps be backed up? They're derived data, but if the operator isn't running during restore, the registry is empty until it regenerates them.
- **Restore ordering**: On cluster restore, the operator must run and regenerate ConfigMaps before the registry server has data. This creates implicit dependencies in disaster recovery procedures.
- **Drift detection**: If a ConfigMap is manually modified or corrupted, there's no single source of truth - the operator will eventually overwrite it, but the intermediate state is inconsistent.

With a direct Kubernetes source, the MCP resources themselves are the source of truth. Standard etcd/Velero backups capture everything needed; on restore, the registry server simply queries the restored resources.

#### Two-component coordination

Splitting registry population across operator and registry server introduces:

- **Deployment coupling**: Both components must be healthy for the registry to be populated
- **Version skew**: Operator and registry server must agree on ConfigMap schema/format
- **Debugging complexity**: "Why isn't my server in the registry?" requires checking both operator logs and ConfigMap contents
- **Race conditions**: Operator writes ConfigMap, registry server reads it - timing windows where data is stale or partially written

#### Additional latency

Changes propagate through two hops:
1. MCP resource changes → Operator detects → Operator writes ConfigMap
2. ConfigMap changes → Registry server detects → Registry updates

Each hop adds its own reconciliation interval. With a direct source, there's a single sync interval from resource to registry.

## Future Work: Watch-based Updates

The initial implementation uses interval-based polling via `syncPolicy.interval`, consistent with other source types. However, Kubernetes resources can change frequently, and polling introduces latency between a resource change and registry update.

A future enhancement would add watch-based (informer) support for the kubernetes source:

### Proposed Approach

1. **Shared Informer Factory**: Use `controller-runtime`'s cache/informer infrastructure to watch MCP resources
2. **Event-driven sync**: On resource add/update/delete events, trigger a registry rebuild
3. **Debouncing**: Batch rapid changes (e.g., during deployments) with a short debounce window (e.g., 500ms-2s) to avoid excessive rebuilds
4. **Hybrid mode**: Keep `syncPolicy.interval` as a fallback/consistency check, but primarily react to watch events

### Configuration Extension

```yaml
kubernetes:
  namespaces: []
  labelSelector: ""

  # Future: watch-based sync
  watch:
    enabled: true
    debounceInterval: "1s"  # batch changes within this window
```

### Benefits

- **Near real-time updates**: Registry reflects changes within seconds instead of waiting for next poll interval
- **Reduced API load**: No need for frequent polling; only react to actual changes
- **Consistency**: Informers maintain a local cache, reducing API server load

### Considerations

- **Complexity**: Informer lifecycle management, reconnection handling, cache synchronization
- **Memory**: Informer cache consumes memory proportional to watched resources
- **Startup**: Initial cache sync before serving requests

This can be implemented as a backward-compatible enhancement - existing poll-based configs continue to work, watch mode is opt-in.

## Implementation Plan

1. Add `KubernetesConfig` to `internal/config/config.go`
2. Add config validation for kubernetes source type
3. Implement `KubernetesRegistryHandler` in `internal/sources/kubernetes.go`
4. Register handler in `internal/sources/factory.go`
5. Add unit tests with mock K8s client
6. Add integration test with envtest
7. Update documentation and examples
8. Add Helm chart RBAC templates

## Open Questions

1. **Feature flag**: Should this be behind a feature flag initially?
2. **CRD availability**: How should the handler behave if ToolHive CRDs aren't installed in the cluster?
3. **Cross-cluster**: Should we support watching resources in remote clusters (via kubeconfig)?

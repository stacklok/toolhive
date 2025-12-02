# Proposal: Kubernetes Source Type for Registry Server

## Status

**Proposed** - Supersedes [toolhive#2591](https://github.com/stacklok/toolhive/pull/2591)

## Summary

Add a native `kubernetes` source type to the ToolHive Registry Server that directly watches Kubernetes resources (MCPServer, MCPRemoteProxy, VirtualMCPServer) and builds registry entries from annotated resources. This eliminates the need for an intermediate ConfigMap-based approach.

This functionality is **optional** and must be explicitly enabled by the system administrator through configuration. The registry server can operate without Kubernetes integration, using only git, api, or file sources. Administrators choose which source types to configure based on their deployment environment.

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
┌──────────────────────────────────────────────────────────────────────┐
│                       Registry API Server                            │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │                  KubernetesRegistryHandler                     │  │
│  │                                                                │  │
│  │  Method 1: Direct MCP Resource Annotation                      │  │
│  │  ┌──────────────────────────────────────────────────────────┐  │  │
│  │  │ List MCP resources with registry-export: "true"          │  │  │
│  │  │ Read registry-url annotation → endpoint URL              │  │  │
│  │  └──────────────────────────────────────────────────────────┘  │  │
│  │                                                                │  │
│  │  Method 2: HTTPRoute Annotation (Gateway API)                  │  │
│  │  ┌──────────────────────────────────────────────────────────┐  │  │
│  │  │ List HTTPRoutes with registry-export: "true"             │  │  │
│  │  │ Traverse: HTTPRoute → Service → MCP resource (metadata)  │  │  │
│  │  │ Traverse: HTTPRoute → Gateway (endpoint URL)             │  │  │
│  │  └──────────────────────────────────────────────────────────┘  │  │
│  │                                                                │  │
│  │  Merge results → Build UpstreamRegistry → Return FetchResult   │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                              │                                       │
│                              ▼                                       │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │     SyncManager + StorageManager (existing infrastructure)     │  │
│  └────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────┘
```

### Configuration

```yaml
registryName: "toolhive-cluster"

registries:
  - name: "cluster-mcp-servers"
    # format is optional for kubernetes source - always produces upstream format
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

**Note on format:** The `format` field is optional for the kubernetes source type. Since the handler builds registry entries dynamically from MCP resources (rather than parsing files), it always produces entries in upstream MCP Registry format.

### Registry Export Methods

There are two ways to export MCP resources to the registry:

#### Method 1: Direct MCP Resource Annotation

Annotate the MCPServer, MCPRemoteProxy, or VirtualMCPServer directly with all required information.

| Annotation | Required | Description |
|------------|----------|-------------|
| `toolhive.stacklok.dev/registry-export` | Yes | Must be `"true"` to include in registry |
| `toolhive.stacklok.dev/registry-url` | Yes | The external endpoint URL for this server |
| `toolhive.stacklok.dev/registry-description` | No | Override the description in registry |
| `toolhive.stacklok.dev/registry-tier` | No | Server tier classification |

**Example:**

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

This method is straightforward and requires no Gateway API. Use it when:
- You don't use Gateway API
- You want explicit control over the endpoint URL
- The endpoint URL can't be derived automatically

#### Method 2: HTTPRoute Annotation (Gateway API)

Annotate an HTTPRoute that routes to the MCP resource. The handler automatically discovers the endpoint URL and MCP resource metadata through object traversal.

| Annotation | Required | Description |
|------------|----------|-------------|
| `toolhive.stacklok.dev/registry-export` | Yes | Must be `"true"` on the HTTPRoute |

The handler performs the following traversal:

```
HTTPRoute (annotated)
    │
    ├──→ backendRefs → Service → MCP resource (owner)
    │                              └─→ metadata for registry entry
    │
    └──→ parentRefs → Gateway
                        └─→ listener address + HTTPRoute hostname/path
                              └─→ endpoint URL
```

**Example:**

```yaml
# MCPServer - no annotations needed
apiVersion: toolhive.stacklok.dev/v1alpha1
kind: MCPServer
metadata:
  name: my-mcp-server
  namespace: production
spec:
  # ... creates a Service named my-mcp-server

---
# HTTPRoute - only needs registry-export annotation
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-mcp-server-route
  namespace: production
  annotations:
    toolhive.stacklok.dev/registry-export: "true"
spec:
  parentRefs:
    - name: main-gateway
      namespace: gateway-system
  hostnames:
    - "mcp.example.com"
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /servers/my-mcp-server
      filters:
        # Rewrite the path so the backend receives "/" instead of "/servers/my-mcp-server"
        - type: URLRewrite
          urlRewrite:
            path:
              type: ReplacePrefixMatch
              replacePrefixMatch: /
      backendRefs:
        - name: my-mcp-server
          port: 8080

---
# Gateway - provides the external address
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: main-gateway
  namespace: gateway-system
status:
  addresses:
    - type: Hostname
      value: "mcp.example.com"
```

The handler discovers endpoint: `https://mcp.example.com/servers/my-mcp-server`

This method requires Gateway API but minimizes annotation burden. Use it when:
- You use Gateway API for ingress
- You want automatic endpoint URL discovery
- You prefer annotating routing resources rather than MCP resources

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

1. **FetchRegistry**:
   - List MCP resources (MCPServer, MCPRemoteProxy, VirtualMCPServer) with `registry-export: "true"` annotation and `registry-url` set → build entries directly
   - List HTTPRoutes with `registry-export: "true"` annotation → traverse to MCP resource and Gateway → build entries
   - Merge results, deduplicating by MCP resource (direct annotation takes precedence)
   - Return `UpstreamRegistry` with all discovered servers
2. **CurrentHash**: Same discovery logic, computes hash for change detection
3. **Validate**: Validates kubernetes config (label selector syntax, etc.)

### Sync Behavior

Uses the existing `SyncManager` infrastructure:

1. Every `syncPolicy.interval`, check if sync is needed via `CurrentHash()`
2. If hash changed, call `FetchRegistry()` to get full data
3. Store result via `StorageManager` (file or database)

This is identical to how git, api, and file sources work today.

### RBAC Requirements

The registry server's ServiceAccount needs read access to MCP resources, Services, and Gateway API resources:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: toolhive-registry-reader
rules:
  # MCP resources (for direct annotation method and metadata lookup)
  - apiGroups: ["toolhive.stacklok.dev"]
    resources: ["mcpservers", "mcpremoteproxies", "virtualmcpservers"]
    verbs: ["get", "list", "watch"]
  # Services (to look up ownerReferences and find the MCP resource that created the service)
  # Note: Only the proxy Service has ownerRef to MCPServer; headless services don't
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get"]
  # Gateway API resources (for HTTPRoute annotation method)
  - apiGroups: ["gateway.networking.k8s.io"]
    resources: ["httproutes", "gateways"]
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

For namespace-scoped deployments, use Role/RoleBinding instead. Note that Gateway API permissions are only needed if using the HTTPRoute annotation method.

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

## Future Work: Leader Election for High Availability

When deploying multiple registry server replicas for high availability, leader election ensures only one replica actively syncs from Kubernetes resources. Without coordination, multiple replicas would:

- All poll/watch the same Kubernetes resources (redundant API server load)
- Race to write to the shared database
- Potentially cause inconsistent state during concurrent writes

### Recommended Approach: Lease-based Leader Election

Kubernetes [Leases](https://kubernetes.io/docs/concepts/architecture/leases/) are the recommended mechanism for leader election:

- **Purpose-built**: Lighter than ConfigMaps, designed specifically for coordination
- **Well-tested**: GA since Kubernetes 1.17
- **Native support**: `controller-runtime` uses Leases by default

### Configuration

Since the registry server uses `controller-runtime`, leader election is straightforward:

```go
mgr, err := ctrl.NewManager(cfg, ctrl.Options{
    LeaderElection:          true,
    LeaderElectionID:        "toolhive-registry-kubernetes-source",
    LeaderElectionNamespace: "toolhive-system",
    // Defaults are typically appropriate:
    // LeaseDuration: 15s - how long the lease is valid
    // RenewDeadline: 10s - how long leader retries renewal
    // RetryPeriod:   2s  - how often non-leaders check
})
```

### Behavior with Multiple Replicas

| Replica Role | Kubernetes Sync | Database Reads | Database Writes |
|--------------|-----------------|----------------|-----------------|
| Leader | Active | Yes | Yes |
| Non-leader | Inactive (standby) | Yes | No |

- **Leader**: Actively syncs from Kubernetes, writes to database
- **Non-leaders**: Serve read traffic from the shared database, wait for leadership
- **Failover**: If leader fails, another replica acquires the lease within ~15-20 seconds

### Best Practices

1. **Health checks**: Integrate leader election status with `/healthz` endpoints
2. **Unique election ID**: Include component name to avoid collisions with other controllers
3. **Monitor transitions**: Track leadership changes; frequent transitions may indicate configuration issues
4. **Clock synchronization**: Ensure NTP or similar is configured across nodes

### Future: Coordinated Leader Election

Kubernetes 1.31+ introduced [Coordinated Leader Election](https://kubernetes.io/docs/concepts/cluster-administration/coordinated-leader-election/) which provides smoother leadership handoff during rolling updates. As of Kubernetes 1.34, this feature is still beta and disabled by default.

Once Coordinated Leader Election reaches GA, the registry server could adopt it for:
- New pod becoming leader *before* old pod terminates
- Reduced sync gaps during deployments
- Version-aware leader selection via `LeaseCandidate` resources

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

# K8s-Aware vMCP with Dynamic Backend Discovery

## Problem Statement

The vMCP implementation currently has duplicated logic between the operator
and vMCP server. The operator discovers backends, resolves
ExternalAuthConfigs, and embeds backend configuration into a ConfigMap.
Meanwhile, the vMCP server has its own discovery code in
`pkg/vmcp/workloads/k8s.go` but only loads config statically from the
ConfigMap at startup.

This duplication creates several problems. Apart from the code duplication issue backend
changes require an operator reconcile, ConfigMap update, and pod restart
before they take effect. Also, the operator has grown complex with auth
resolution logic scattered across components.

## Goals

- Make vMCP server K8s-aware with dynamic backend discovery via informers
- Move auth resolution from operator to vMCP server
- Simplify operator to handle only infrastructure management
- Enable dynamic updates without pod restarts when backends change
- Maintain CLI mode behavior (static discovery at startup)

## Non-Goals

- Multi-replica caching strategies (Redis, sticky sessions)
- Session capability refresh via MCP notifications
- CLI mode changes (remains static one-time discovery)
- Prometheus metrics for discovered backends (tracked separately)

## Design

### Architecture Overview

Today the operator watches VirtualMCPServer resources, discovers backends,
resolves ExternalAuthConfigs, embeds backend configuration in a ConfigMap, and
reconciles on MCPServer changes. The vMCP server simply loads config from the
ConfigMap and routes requests, though it has duplicate discovery code it
doesn't use dynamically.

The target architecture flips these responsibilities. The operator becomes a
"dumb" infrastructure manager that watches VirtualMCPServer, writes minimal
static config to ConfigMap, creates RBAC, manages Deployment and Service, and
updates CRD status. The vMCP server becomes "smart" by running a
controller-runtime manager, watching MCPServer/ExternalAuthConfig/Secret via
informers, owning all backend discovery and auth resolution, and gating
readiness on informer sync.

### Status Reporting Mechanism

One design decision is how the operator should learn about vMCP's discovered
backends to update the VirtualMCPServer CRD status. There are two main
approaches to consider, both with pros and cons:

**Option A: HTTP Polling**

In this approach, vMCP exposes a `/status` endpoint that returns discovered
backend information. The operator periodically polls this endpoint and updates
the CRD status.

Advantages:
- vMCP's RBAC stays minimal (read-only access to K8s resources)
- Clear separation of concerns - vMCP doesn't need to know about CRD status
- Simpler vMCP implementation

Disadvantages:
- Polling latency
- Operator needs HTTP client logic and retry handling

**Option B: Direct CRD Writes (StatusReporter)**

In this approach, vMCP writes directly to the VirtualMCPServer CRD status
using a StatusReporter pattern. The operator observes status changes via its
existing watch.

Advantages:
- Real-time status updates
- No polling overhead
- Operator reconciliation triggers naturally on status changes

Disadvantages:
- vMCP needs write RBAC permissions for VirtualMCPServer/status
- Tighter coupling between vMCP and operator CRDs
- vMCP needs K8s client configured for CRD access

I'm leaning implementing the simple polling first also because the status
doesn't have any sensitive data and migrating to full StatusReporter
subsequently.

### Secret Handling

For backend authentication, secrets are fetched directly by vMCP via the K8s
API when resolving ExternalAuthConfig references.

**Security boundary: same-namespace only**

This approach is secure because secret access is constrained to the vMCP's own
namespace through two mechanisms:

1. **RBAC scoping**: The operator creates a namespace-scoped `Role` (not
   `ClusterRole`) with `get`, `list`, `watch` permissions on secrets. The
   `RoleBinding` binds this to the vMCP's `ServiceAccount` in the same
   namespace. vMCP cannot access secrets in other namespaces.

2. **API design**: The `SecretKeyRef` type used by `MCPExternalAuthConfig` has
   no `Namespace` field - only `Name` and `Key`. Secret lookups always use the
   parent resource's namespace, preventing cross-namespace references at the
   schema level.

**Secret change propagation**

Secret changes are handled using the `Watches()` pattern with mapping
functions, which is the idiomatic controller-runtime approach:

```go
Watches(
    &corev1.Secret{},
    handler.EnqueueRequestsFromMapFunc(w.secretToMCPServers),
)
```

When a Secret changes, the mapping function finds MCPServers that reference it
(via their ExternalAuthConfig) and enqueues reconcile requests. The reconciler
then re-fetches the secret and updates the backend's auth configuration. This
provides automatic secret rotation without pod restarts.

The existing RBAC rules in `virtualmcpserver_deployment.go` already grant the
necessary permissions, and `pkg/vmcp/workloads/k8s.go` demonstrates the
discovery pattern.

### Dynamic Registry

The current `immutableRegistry` discovers backends once at startup and never
changes. For dynamic discovery, we need a mutable registry with thread-safe
Upsert/Remove operations and a version counter for cache invalidation.

Since we're using the reconciler pattern, event callbacks are unnecessary. The
reconciler already handles "what changed" logic, and consumers can simply
check the version counter to detect changes. This keeps the interface simple
and idiomatic for controller-runtime usage.

The registry maintains a version counter incremented on any mutation. The
discovery manager includes this version in its cache key, so any backend
change automatically invalidates all cached capabilities without explicit
invalidation logic.

### Startup Synchronization

The readiness probe gates traffic until the controller-runtime cache has
synced via `WaitForCacheSync()`. This guarantees that informers have received
their initial list from the API server and the local cache is populated.

There is a brief window after cache sync where reconcilers haven't yet
processed all queued events. However, tracking reconciliation completion adds
significant complexity for minimal benefit. Production controllers like
cert-manager and ArgoCD follow the same pattern - they rely on cache sync, not
reconciliation tracking. Kubernetes is eventually consistent, and clients
already handle scenarios where discovery returns incomplete results.

If the first request arrives during this window, the client may see fewer
backends than expected. This is transient and self-correcting - backends
appear within seconds as reconcilers process the queue. A startup probe with
generous timeout handles slow-starting scenarios.

### Config Changes

When VirtualMCPServer spec changes, such as the `groupRef` field, the current
behavior triggers a pod restart via ConfigMap checksum changes. This behavior
should be preserved. Config changes are fundamental, not runtime state, and
changing things like `groupRef` mid-flight would require complex watch
management. Auth middleware and conflict resolution are wired at startup, so
existing sessions would have stale information if these changed dynamically.

### CLI Mode

CLI mode should remain static with one-time discovery at startup. Local
development sessions are typically short-lived, and adding dynamic discovery
to CLI would add complexity for limited benefit. This can be revisited as a
separate work item if there's demand.

## Implementation

### Phase 1: Add /status Endpoint

Add a detailed status endpoint to vMCP. This phase can be merged independently
and provides value regardless of whether the operator uses polling or
StatusReporter, as the endpoint is useful for debugging and monitoring.

The endpoint at `GET /status` (unauthenticated) returns:

```go
type StatusResponse struct {
    Backends []BackendStatus `json:"backends"`
    Healthy  bool            `json:"healthy"`
    Version  string          `json:"version"`
    GroupRef string          `json:"group_ref"`
}

type BackendStatus struct {
    Name      string `json:"name"`
    Endpoint  string `json:"endpoint"`
    Health    string `json:"health"`    // "healthy", "degraded", "unhealthy", "unknown"
    Transport string `json:"transport"`
    AuthType  string `json:"auth_type,omitempty"` // "unauthenticated", "header_injection", "token_exchange"
}
```

All fields map directly to existing types: `Backend.Name`, `Backend.BaseURL`,
`Backend.HealthStatus`, `Backend.TransportType`, and
`Backend.AuthConfig.Type`.

Files to create:
- `pkg/vmcp/server/status.go` - Status endpoint handler and types

Files to modify:
- `pkg/vmcp/server/server.go` - Register `/status` endpoint alongside existing `/health` and `/ping`

### Phase 2: Create Mutable Backend Registry

Replace `immutableRegistry` with a thread-safe `DynamicRegistry` that supports
dynamic updates. This is foundational for the reconciler work in Phase 3.

The interface extends the existing `BackendRegistry` with simple, idempotent
mutation operations:

```go
type DynamicRegistry interface {
    BackendRegistry  // Embed existing interface (Get, List, Count)

    // Upsert adds or updates a backend atomically.
    // Idempotent - calling with the same backend multiple times is safe.
    // Increments Version() on every call.
    // Returns error only for validation failures (nil backend, empty ID).
    Upsert(backend *Backend) error

    // Remove deletes a backend by ID.
    // Idempotent - removing non-existent backends returns nil.
    // Increments Version() on every call.
    Remove(backendID string) error

    // Version returns the current registry version.
    // Increments on every mutation (Upsert/Remove).
    // Used for cache invalidation in discovery manager.
    Version() uint64
}
```

The `Upsert` and `Remove` methods are idempotent, aligning with the Kubernetes
reconciler pattern. No event callbacks are needed since version-based cache
invalidation is sufficient.

```go
type dynamicRegistry struct {
    mu       sync.RWMutex
    backends map[string]*Backend
    version  uint64
}
```

The implementation uses a read-write mutex protecting both the backends map
and version counter. Read operations (Get, List, Count, Version) take a read
lock, while mutations (Upsert, Remove) take a write lock and increment the
version.

Cache invalidation integration: The discovery manager maintains per-user cache
entries for capability isolation (different users may have different
permissions or see different backend subsets). Each cache entry is tagged with
the registry version at computation time:

```go
type cacheEntry struct {
    capabilities    *AggregatedCapabilities
    expiresAt       time.Time
    registryVersion uint64  // Version when this entry was computed
}

func (m *DefaultManager) GetCapabilities(ctx context.Context, userID string, backends []Backend) (*AggregatedCapabilities, error) {
    currentVersion := m.registry.Version()
    cacheKey := m.generateCacheKey(userID, backends)

    m.cacheMu.RLock()
    entry, exists := m.cache[cacheKey]
    m.cacheMu.RUnlock()

    // Cache hit: valid if not expired AND registry hasn't changed
    if exists && time.Now().Before(entry.expiresAt) && entry.registryVersion == currentVersion {
        return entry.capabilities, nil
    }

    // Cache miss or stale: recompute capabilities
    capabilities, err := m.computeCapabilities(ctx, userID, backends)
    if err != nil {
        return nil, err
    }

    // Store with current version
    m.cacheMu.Lock()
    m.cache[cacheKey] = &cacheEntry{
        capabilities:    capabilities,
        expiresAt:       time.Now().Add(m.cacheTTL),
        registryVersion: currentVersion,
    }
    m.cacheMu.Unlock()

    return capabilities, nil
}
```

This approach provides lazy invalidation - entries aren't eagerly removed on
registry mutation, but are recomputed on next access if the version has
changed. This avoids thundering herd problems where all users simultaneously
recompute after any backend change.

Files to modify:
- `pkg/vmcp/registry.go` - Add `DynamicRegistry` interface and `dynamicRegistry` implementation
- `pkg/vmcp/discovery/manager.go` - Add `registryVersion` field to cache entries and version check on lookup

### Phase 3: Add Reconciler Infrastructure

Create a new `pkg/vmcp/k8s/` package with controller-runtime integration for
Kubernetes mode. We use the reconciler pattern rather than direct informer
event handlers because it provides built-in retry with exponential backoff,
rate limiting, event deduplication, and cleaner error handling.

The manager wraps controller-runtime:

```go
type Manager struct {
    ctrlManager  ctrl.Manager
    namespace    string
    groupRef     string
    registry     vmcp.DynamicRegistry
}

func NewManager(cfg *rest.Config, namespace, groupRef string, registry vmcp.DynamicRegistry) (*Manager, error)
func (m *Manager) Start(ctx context.Context) error
func (m *Manager) WaitForCacheSync(ctx context.Context) bool
```

The MCPServer watcher uses the standard reconciler pattern. It filters by
groupRef and converts MCPServer resources to Backend structs:

```go
type MCPServerWatcher struct {
    client   client.Client
    registry vmcp.DynamicRegistry
    groupRef string
}

func (w *MCPServerWatcher) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // Use NamespacedName as consistent backend ID
    backendID := req.NamespacedName.String()

    var mcpServer mcpv1alpha1.MCPServer
    if err := w.client.Get(ctx, req.NamespacedName, &mcpServer); err != nil {
        if apierrors.IsNotFound(err) {
            // Resource deleted - remove from registry (idempotent)
            w.registry.Remove(backendID)
            return ctrl.Result{}, nil
        }
        return ctrl.Result{}, err  // Will retry with backoff
    }

    // Filter by group
    if mcpServer.Spec.GroupRef != w.groupRef {
        return ctrl.Result{}, nil
    }

    // Convert and upsert into registry
    backend, err := w.convertToBackend(ctx, &mcpServer)
    if err != nil {
        return ctrl.Result{}, err  // Will retry with backoff
    }
    backend.ID = backendID  // Use consistent ID
    if err := w.registry.Upsert(backend); err != nil {
        return ctrl.Result{}, err
    }

    return ctrl.Result{}, nil
}

func (w *MCPServerWatcher) SetupWithManager(mgr ctrl.Manager) error {
    // Handler for ExternalAuthConfig changes - triggers MCPServer reconciliation
    externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
        func(ctx context.Context, obj client.Object) []reconcile.Request {
            // Find MCPServers referencing this ExternalAuthConfig
            // Return reconcile requests for affected MCPServers
        },
    )

    // Handler for Secret changes - triggers MCPServer reconciliation
    secretHandler := handler.EnqueueRequestsFromMapFunc(
        func(ctx context.Context, obj client.Object) []reconcile.Request {
            secret := obj.(*corev1.Secret)
            // Find MCPServers that reference this secret via ExternalAuthConfig
            // This is O(n) per secret change, acceptable for typical cluster sizes
            // Return reconcile requests for affected MCPServers
        },
    )

    return ctrl.NewControllerManagedBy(mgr).
        For(&mcpv1alpha1.MCPServer{}).
        Watches(&mcpv1alpha1.MCPExternalAuthConfig{}, externalAuthConfigHandler).
        Watches(&corev1.Secret{}, secretHandler).
        Complete(w)
}
```

Using `Watches()` with mapping functions is the idiomatic controller-runtime
pattern for "when resource A changes, reconcile related resource B". This
avoids the need for separate reconcilers or field indexing. When a Secret or
ExternalAuthConfig changes, the mapping function finds affected MCPServers and
enqueues reconcile requests for them. The MCPServerWatcher then re-resolves
auth during its normal reconciliation.

Readiness probe integration - the server's readiness endpoint checks cache sync:

```go
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
    if s.k8sManager != nil {
        ctx, cancel := context.WithTimeout(r.Context(), time.Second)
        defer cancel()
        if !s.k8sManager.WaitForCacheSync(ctx) {
            w.WriteHeader(http.StatusServiceUnavailable)
            json.NewEncoder(w).Encode(map[string]string{"status": "cache syncing"})
            return
        }
    }
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
}
```

Files to create:
- `pkg/vmcp/k8s/manager.go` - Controller-runtime manager wrapper
- `pkg/vmcp/k8s/mcpserver_watcher.go` - MCPServer reconciler with Watches() for related resources

Files to modify:
- `pkg/vmcp/server/server.go` - Initialize K8s manager in K8s mode, add `/readyz` endpoint
- `cmd/vmcp/app/commands.go` - Detect K8s mode, create and start manager goroutine

### Phase 4: Move Auth Resolution to vMCP

Refactor the existing auth resolution code for use by the reconcilers. The code in `pkg/vmcp/workloads/k8s.go` already implements discovery with `discoverAuthConfig()` and the converters in `pkg/vmcp/auth/converters/` handle the actual resolution.

**Secret Handling Approach**: vMCP fetches secrets at runtime via the Kubernetes API rather than relying on env var mounting. This approach is preferred because:
- **Zero-downtime rotation**: Secret changes propagate in seconds via watches, no pod restart needed
- **Better security**: Secrets never appear in pod specs or `kubectl describe pod` output
- **Dynamic alignment**: Matches the dynamic backend discovery model - new backends work without pod restarts
- **K8s idiomatic**: Controller pattern with watches is standard for dynamic configuration

Secrets are not cached separately - the resolved credentials are stored in the `Backend` struct in the DynamicRegistry. When a Secret changes, the watch triggers MCPServer reconciliation, which re-fetches and re-resolves credentials.

The flow changes from operator-driven to reconciler-driven:

Current (Operator):
1. Operator reads MCPServer.spec.externalAuthConfigRef
2. Operator fetches MCPExternalAuthConfig CRD
3. Operator generates env var names and mounts Secrets as environment variables
4. Operator embeds env var references in ConfigMap

New (vMCP):
1. MCPServer reconciler triggered by new/updated MCPServer (or by Secret/ExternalAuthConfig change via Watches())
2. Reconciler reads externalAuthConfigRef from MCPServer spec
3. Reconciler fetches MCPExternalAuthConfig via K8s client
4. Reconciler fetches referenced Secret via K8s client, resolving actual credential values
5. Reconciler stores resolved auth strategy (with credentials in memory) in Backend, upserts to DynamicRegistry

When a Secret or ExternalAuthConfig changes, the Watches() mapping function finds affected MCPServers and enqueues them for reconciliation, triggering steps 1-5 above.

Files to modify:
- `pkg/vmcp/workloads/k8s.go` - Refactor `discoverAuthConfig()` for use by reconcilers
- `pkg/vmcp/auth/converters/` - Ensure converters work with vMCP's K8s client context

### Phase 5: Simplify Operator

Remove backend discovery and auth resolution from the operator. The operator's role becomes purely infrastructure management.

ConfigMap content is simplified to contain only static configuration:

```yaml
name: my-vmcp-server
group_ref: my-group
namespace: default
incoming_auth:
  type: oidc
  oidc:
    issuer: https://auth.example.com
    client_id: my-client
    client_secret_env: VMCP_OIDC_CLIENT_SECRET
    audience: my-audience
outgoing_auth:
  source: discovered  # vMCP discovers at runtime
aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
composite_tools: [...]
```

Code to remove from operator:
- `discoverBackends()` function in `virtualmcpserver_controller.go`
- `buildOutgoingAuthConfig()` function
- Complex OutgoingAuth.Backends resolution in `vmcpconfig/converter.go`

Status reporting implementation depends on the chosen approach:

**If HTTP Polling (Option A):**

Add polling logic to the reconciler:

```go
func (r *VirtualMCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ... existing resource management ...

    status, err := r.pollVMCPStatus(ctx, vmcp)
    if err != nil {
        statusManager.SetCondition("StatusPolling", "PollFailed", err.Error(), metav1.ConditionFalse)
        // Preserve last known DiscoveredBackends
        return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
    }

    statusManager.SetDiscoveredBackends(convertStatusToDiscoveredBackends(status.Backends))
    statusManager.SetCondition("StatusPolling", "PollSucceeded", "", metav1.ConditionTrue)
    return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}
```

**If StatusReporter (Option B):**

No polling logic needed. vMCP writes status directly. Operator just observes changes via existing watch.

Files to modify:
- `cmd/thv-operator/controllers/virtualmcpserver_controller.go` - Remove discovery, add status reading (approach TBD)
- `cmd/thv-operator/controllers/virtualmcpserver_vmcpconfig.go` - Simplify config generation
- `cmd/thv-operator/pkg/vmcpconfig/converter.go` - Remove OutgoingAuth.Backends resolution

### Phase 6: Verify RBAC

Verify vMCP pod has necessary RBAC permissions. The existing rules should be sufficient for read-only access:

```go
vmcpRBACRules = []rbacv1.PolicyRule{
    {
        APIGroups: []string{""},
        Resources: []string{"configmaps", "secrets"},
        Verbs:     []string{"get", "list", "watch"},
    },
    {
        APIGroups: []string{"toolhive.stacklok.dev"},
        Resources: []string{"mcpgroups", "mcpservers", "mcpexternalauthconfigs", "mcptoolconfigs"},
        Verbs:     []string{"get", "list", "watch"},
    },
}
```

If StatusReporter (Option B) is chosen, additional RBAC is needed:

```go
{
    APIGroups: []string{"toolhive.stacklok.dev"},
    Resources: []string{"virtualmcpservers/status"},
    Verbs:     []string{"update", "patch"},
}
```

Files to verify/modify:
- `cmd/thv-operator/controllers/virtualmcpserver_controller.go` - Check existing RBAC rules, add status write if needed

## Open Questions

**Status Reporting Mechanism**: HTTP polling vs StatusReporter is the primary open question. Polling is simpler with minimal RBAC but introduces 30-60 second latency. StatusReporter provides real-time updates but requires write RBAC and tighter coupling. This should be decided during design review.

**Polling Intervals** (if Option A): Recommendation is 60 seconds on success, 30 seconds on failure. Should these be configurable?

**Status Endpoint Authentication**: Recommendation is unauthenticated since it exposes operational data not secrets. Should this be revisited?

## Testing

Unit tests should cover the DynamicRegistry operations and events, status endpoint handler, and cache invalidation with version changes.

Integration tests should use envtest to verify reconciler behavior with a real API server. This includes MCPServer reconciliation, Secret change propagation, auth resolution end-to-end, and registry to discovery manager cache invalidation.

E2E tests should cover the full flow with operator and vMCP pod, dynamic backend addition and removal, secret rotation detection, and readiness probe gating during startup.

## Future Enhancements

Multi-replica caching with Redis or sticky sessions, session capability refresh via MCP notifications, and dynamic CLI mode are potential future enhancements but out of scope for this work.

# vMCP Status Reporter

Platform-agnostic status reporting for Virtual MCP servers.

## Overview

The StatusReporter abstraction enables vMCP runtime to report operational status back to the control plane (Kubernetes operator or CLI state manager). This allows the runtime to autonomously update backend discovery results, health status, and operational state without relying on the controller to infer it through polling.

## Why StatusReporter?

**Problem**: Currently, the Kubernetes operator discovers backends and updates VirtualMCPServer status. This creates duplicate discovery work when vMCP runtime also needs to discover backends for capability aggregation (Issue #3004).

**Solution**: vMCP runtime discovers backends and reports the results back via StatusReporter. The operator reads this reported status instead of discovering backends itself.

## Architecture

```
┌─────────────────────────────────────────┐
│ vMCP Runtime                            │
│  - Discovers backends from MCPGroup     │
│  - Aggregates capabilities              │
│  - Reports status via StatusReporter    │
└──────────────┬──────────────────────────┘
               │
               ├─ Kubernetes Mode ──────────────┐
               │                                 │
               │  ┌───────────────────────────┐ │
               │  │ K8SReporter               │ │
               │  │  - Updates VirtualMCPServer │
               │  │    .Status via K8s API    │ │
               │  │  - Requires RBAC          │ │
               │  └───────────────────────────┘ │
               │                                 │
               └─ CLI Mode ─────────────────────┤
                                                 │
                  ┌───────────────────────────┐ │
                  │ NoOpReporter              │ │
                  │  - Silent (no persistence)│ │
                  │  - Optional debug logging │ │
                  └───────────────────────────┘ │
                                                 │
┌────────────────────────────────────────────────┘
│ Control Plane (reads status)
│
├─ Kubernetes: kubectl describe vmcp
└─ CLI: thv status (future)
```

## Implementations

### K8SReporter

Updates `VirtualMCPServer.Status` in Kubernetes cluster.

**Use when**: Running vMCP as a Kubernetes Deployment managed by the operator.

**Requirements**:
- ServiceAccount with RBAC permissions (see [RBAC.md](./RBAC.md))
- Kubernetes client

**Example**:
```go
import (
    "sigs.k8s.io/controller-runtime/pkg/client"
    vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// Create reporter
reporter, err := vmcpstatus.NewK8SReporter(vmcpstatus.K8SReporterConfig{
    Client:           k8sClient,
    Name:             "my-vmcp",
    Namespace:        "default",
    PeriodicInterval: 30 * time.Second, // Optional: periodic reporting
})

// Start reporter
if err := reporter.Start(ctx); err != nil {
    log.Fatal(err)
}

// Report status
status := &vmcpstatus.Status{
    Phase:   vmcpstatus.PhaseReady,
    Message: "Server is ready",
    DiscoveredBackends: []vmcpstatus.DiscoveredBackend{
        {
            Name:            "backend1",
            URL:             "http://backend1:8080",
            Status:          vmcpstatus.BackendStatusReady,
            AuthType:        "oauth2",
            AuthConfigRef:   "auth-config-1",
            LastHealthCheck: time.Now(),
        },
    },
    ObservedGeneration: vmcp.Generation,
    Timestamp:          time.Now(),
}

if err := reporter.ReportStatus(ctx, status); err != nil {
    log.Error("Failed to report status:", err)
}

// Stop reporter on shutdown
defer reporter.Stop(ctx)
```

### NoOpReporter

Silent implementation for CLI mode.

**Use when**: Running vMCP as a standalone CLI application.

**Example**:
```go
// Create no-op reporter (optionally enable debug logging)
reporter := vmcpstatus.NewNoOpReporter(true) // true = log updates

// Start/Stop are no-ops
reporter.Start(ctx)
reporter.ReportStatus(ctx, status) // Logs but doesn't persist
reporter.Stop(ctx)
```

## Status Model

The `Status` struct is platform-agnostic and maps to:
- `VirtualMCPServer.Status` (Kubernetes)
- File-based state (CLI, future)
- Metrics/observability systems (future)

```go
type Status struct {
    // Phase: Pending, Ready, Degraded, Failed
    Phase Phase

    // Human-readable message
    Message string

    // Fine-grained status conditions
    Conditions []Condition

    // Backends discovered by vMCP runtime
    DiscoveredBackends []DiscoveredBackend

    // Tracks which spec generation this status reflects
    ObservedGeneration int64

    // When this status was generated
    Timestamp time.Time
}
```

### Backend Discovery Reporting

The key use case for StatusReporter is reporting backend discovery results:

```go
status := &vmcpstatus.Status{
    Phase:   vmcpstatus.PhaseReady,
    Message: "Discovered 3 backends, all healthy",
    DiscoveredBackends: []vmcpstatus.DiscoveredBackend{
        {
            Name:            "backend1",
            URL:             "http://backend1.default.svc:8080/mcp",
            Status:          vmcpstatus.BackendStatusReady,
            AuthType:        "oauth2",
            AuthConfigRef:   "oauth-config",
            LastHealthCheck: time.Now(),
        },
        {
            Name:            "backend2",
            URL:             "http://backend2.default.svc:8080/mcp",
            Status:          vmcpstatus.BackendStatusReady,
            AuthType:        "unauthenticated",
            LastHealthCheck: time.Now(),
        },
        {
            Name:            "backend3",
            URL:             "http://backend3.default.svc:8080/mcp",
            Status:          vmcpstatus.BackendStatusUnavailable,
            Message:         "Connection refused",
            LastHealthCheck: time.Now(),
        },
    },
    Conditions: []vmcpstatus.Condition{
        {
            Type:               vmcpstatus.ConditionTypeBackendsDiscovered,
            Status:             metav1.ConditionTrue,
            Reason:             vmcpstatus.ReasonBackendDiscoverySucceeded,
            Message:            "Discovered 3 backends",
            LastTransitionTime: time.Now(),
        },
    },
    ObservedGeneration: 1,
    Timestamp:          time.Now(),
}
```

## Integration Points

### vMCP Server

The vMCP server is initialized with a StatusReporter:

```go
// In cmd/vmcp or operator deployment
var statusReporter vmcpstatus.Reporter

if runningInKubernetes {
    statusReporter, _ = vmcpstatus.NewK8SReporter(vmcpstatus.K8SReporterConfig{
        Client:    k8sClient,
        Name:      vmcpName,
        Namespace: vmcpNamespace,
    })
} else {
    statusReporter = vmcpstatus.NewNoOpReporter(true)
}

// Create server with reporter
srv, _ := vmcpserver.New(ctx, &vmcpserver.Config{
    Name:           "my-vmcp",
    StatusReporter: statusReporter,
    // ...
}, router, backendClient, discoveryMgr, backends, workflowDefs)

// Server lifecycle manages reporter Start/Stop
srv.Start(ctx)
```

### Backend Discovery (Future - Issue #3004)

When vMCP runtime discovers backends in dynamic mode:

```go
// Discover backends from MCPGroup
backends, err := discoverer.Discover(ctx, groupName)

// Convert to status format
discoveredBackends := make([]vmcpstatus.DiscoveredBackend, len(backends))
for i, backend := range backends {
    discoveredBackends[i] = vmcpstatus.DiscoveredBackend{
        Name:            backend.Name,
        URL:             backend.URL,
        Status:          mapHealthStatus(backend.HealthStatus),
        AuthType:        backend.AuthType,
        AuthConfigRef:   backend.AuthConfigRef,
        LastHealthCheck: time.Now(),
    }
}

// Report to control plane
status := &vmcpstatus.Status{
    Phase:              vmcpstatus.PhaseReady,
    Message:            fmt.Sprintf("Discovered %d backends", len(backends)),
    DiscoveredBackends: discoveredBackends,
    Timestamp:          time.Now(),
}

statusReporter.ReportStatus(ctx, status)
```

## RBAC Requirements

See [RBAC.md](./RBAC.md) for detailed RBAC configuration.

**Minimum required permissions** (Kubernetes mode):
```yaml
- apiGroups: ["toolhive.stacklok.io"]
  resources: ["virtualmcpservers/status"]
  verbs: ["get", "update", "patch"]
```

## Testing

Run the test suite:
```bash
go test ./pkg/vmcp/status/... -v
```

Tests cover:
- K8SReporter status updates
- Condition mapping and merging
- NoOpReporter no-op behavior
- Error handling

## Related Issues

- **#2854**: This implementation (StatusReporter abstraction)
- **#3003**: Mode-aware ConfigMap and RBAC (uses StatusReporter RBAC requirements)
- **#3004**: Remove operator discovery in dynamic mode (depends on StatusReporter)

## Future Enhancements

1. **Metrics Integration**: Report status as Prometheus metrics
2. **Status Caching**: Cache last reported status to avoid redundant updates
3. **Conflict Resolution**: Retry logic for status update conflicts
4. **Event Generation**: Kubernetes Events for status changes
5. **CLI State Persistence**: File-based status reporter for `thv` CLI

# Status Reporting Package

The `status` package provides a platform-agnostic abstraction for vMCP runtime to report operational status.

## Overview

This package enables the vMCP runtime to communicate its operational state to different destinations based on the deployment environment. By implementing the `Reporter` interface, the vMCP runtime can push status updates to:

- Kubernetes status subresources (for operator-managed deployments)
- Log outputs (for CLI/local development)
- Metrics systems (for observability)
- Custom destinations (via user-defined implementations)

## Motivation

Currently, VirtualMCPServer CRD status is populated by the controller through static queries at reconcile time. The vMCP runtime has no way to report actual operational state (backend health, tool counts, connectivity), leading to:

- **Duplicate backend discovery work**: Operator discovers backends, then vMCP runtime discovers them again
- **Delayed status updates**: Operator polls periodically rather than receiving real-time updates
- **No visibility into runtime health**: Controller can only infer status, not observe actual runtime state

The StatusReporter abstraction addresses these issues by enabling direct status reporting from the vMCP runtime.

## Architecture

### Core Components

1. **Reporter Interface** (`reporter.go`): Platform-agnostic interface for status reporting
2. **Status Model** (`types.go`): Comprehensive status data structures
3. **Implementations**:
   - **NoOpReporter** (`noop_reporter.go`): For CLI/local development (current)
   - **K8sReporter** (future): Updates VirtualMCPServer/status subresource
   - **LogReporter** (future): Logs status periodically

### Design Principles

- **Platform Independence**: Core domain logic works for both CLI and Kubernetes
- **Loose Coupling**: Runtime imports only necessary types, not controller code
- **Graceful Degradation**: Reporter errors don't disrupt vMCP runtime
- **Non-blocking**: Status reporting runs in background goroutines

## Status Model

### RuntimeStatus

The `RuntimeStatus` structure captures the complete operational state of a vMCP server:

```go
type RuntimeStatus struct {
    Phase                 Phase              // Operational phase (Ready, Degraded, Failed, etc.)
    Message               string             // Human-readable status message
    Conditions            []Condition        // Fine-grained status conditions
    DiscoveredBackends    []DiscoveredBackend // Backend server information
    TotalToolCount        int                // Total tools after aggregation
    TotalResourceCount    int                // Total resources
    TotalPromptCount      int                // Total prompts
    HealthyBackendCount   int                // Number of healthy backends
    UnhealthyBackendCount int                // Number of unhealthy backends
    DegradedBackendCount  int                // Number of degraded backends
    LastDiscoveryTime     time.Time          // When discovery last completed
    LastUpdateTime        time.Time          // When status was last updated
}
```

### Phase

Represents the high-level operational state:

- **Ready**: vMCP server is fully operational
- **Degraded**: Operational but experiencing issues (some backends unhealthy)
- **Failed**: Not operational due to critical errors
- **Unknown**: Status not yet determined
- **Starting**: Server is initializing

### Condition

Fine-grained status conditions following Kubernetes API conventions:

- **BackendsDiscovered**: Backend discovery completed
- **BackendsHealthy**: All backends are healthy
- **ServerReady**: Server accepting requests
- **CapabilitiesAggregated**: Capability aggregation completed

Each condition has:
- Type (ConditionType)
- Status (True/False/Unknown)
- LastTransitionTime
- Reason (programmatic identifier)
- Message (human-readable details)

### DiscoveredBackend

Information about each discovered backend:

```go
type DiscoveredBackend struct {
    ID            string                  // Unique identifier
    Name          string                  // Human-readable name
    HealthStatus  vmcp.BackendHealthStatus // Current health
    BaseURL       string                  // MCP server URL
    TransportType string                  // MCP transport protocol
    ToolCount     int                     // Tools provided
    ResourceCount int                     // Resources provided
    PromptCount   int                     // Prompts provided
    LastCheckTime time.Time               // Last health check
}
```

## Reporter Interface

```go
type Reporter interface {
    // Report sends a single status update
    Report(ctx context.Context, status *RuntimeStatus) error

    // Start begins periodic status reporting
    Start(ctx context.Context, interval time.Duration, statusFunc func() *RuntimeStatus) error

    // Stop stops periodic reporting and cleans up
    Stop()
}
```

### Usage Patterns

#### One-time Status Report

```go
reporter := status.NewNoOpReporter()
runtimeStatus := &status.RuntimeStatus{
    Phase:   status.PhaseReady,
    Message: "All systems operational",
    // ... other fields
}
err := reporter.Report(ctx, runtimeStatus)
```

#### Periodic Status Reporting

```go
reporter := status.NewNoOpReporter()

// Start periodic reporting
statusFunc := func() *status.RuntimeStatus {
    return getCurrentStatus()
}
err := reporter.Start(ctx, 30*time.Second, statusFunc)

// ... runtime continues ...

// Stop when shutting down
reporter.Stop()
```

## Current Implementation: NoOpReporter

The `NoOpReporter` is a no-operation implementation used for CLI/local development environments where status reporting is not needed.

### Characteristics

- All methods return immediately without error
- No side effects or I/O operations
- Safe for concurrent use
- Zero overhead

### When to Use

- CLI/local development mode
- Testing environments where status reporting is not relevant
- Placeholder during development before implementing actual reporters

### Example

```go
// Create a NoOpReporter for CLI mode
reporter := status.NewNoOpReporter()

// Report status (no-op)
err := reporter.Report(ctx, status)
// err is always nil

// Start periodic reporting (no-op)
err = reporter.Start(ctx, 30*time.Second, statusFunc)
// err is always nil, statusFunc is never called

// Stop (no-op)
reporter.Stop()
```

## Future Implementations

### K8sReporter (Planned)

Will update VirtualMCPServer/status subresource in Kubernetes:

- Authenticates to Kubernetes API server
- Updates `status` subresource of VirtualMCPServer CRD
- Requires RBAC permissions: `virtualmcpservers/status` update/patch
- Handles errors gracefully (falls back to logging)

### LogReporter (Planned)

Will log status updates for observability:

- Logs structured status information
- Configurable log levels and formats
- Useful for debugging and troubleshooting

## Integration with vMCP Runtime

The vMCP runtime will integrate StatusReporter as follows:

1. **Initialization**: Create appropriate Reporter based on environment
2. **Discovery**: Report status after backend discovery completes
3. **Health Monitoring**: Update status when backend health changes
4. **Capability Aggregation**: Report status after aggregation completes
5. **Shutdown**: Stop reporter before exit

Example integration:

```go
// Create reporter based on environment
var reporter status.Reporter
if isKubernetesEnvironment() {
    reporter = status.NewK8sReporter(name, namespace) // Future
} else {
    reporter = status.NewNoOpReporter()
}

// Start periodic reporting
statusFunc := func() *status.RuntimeStatus {
    return vmcpServer.GetCurrentStatus()
}
reporter.Start(ctx, 30*time.Second, statusFunc)

// Report status on significant events
if err := performDiscovery(); err == nil {
    reporter.Report(ctx, getStatusAfterDiscovery())
}

// Stop on shutdown
defer reporter.Stop()
```

## Testing

The package includes comprehensive tests for NoOpReporter:

```bash
# Run tests
go test ./pkg/vmcp/status/...

# Run with coverage
go test -cover ./pkg/vmcp/status/...

# Run with race detector
go test -race ./pkg/vmcp/status/...
```

Test coverage includes:
- Basic functionality (Report, Start, Stop)
- Edge cases (nil status, cancelled context, zero interval)
- Concurrent access
- Full lifecycle testing
- Interface compliance verification

## Dependencies

### Internal

- `github.com/stacklok/toolhive/pkg/vmcp`: For BackendHealthStatus type

### External

- None for the core abstraction
- Future implementations will add dependencies:
  - K8sReporter: `k8s.io/client-go`, `sigs.k8s.io/controller-runtime`
  - LogReporter: `github.com/stacklok/toolhive/pkg/logger`

## Related Documentation

- [Virtual MCP Proposal](../../../docs/proposals/THV-2106-virtual-mcp-server.md)
- [vMCP Package Overview](../doc.go)
- [GitHub Issue #2854](https://github.com/stacklok/toolhive/issues/2854)

## Contributing

When extending this package:

1. **New Implementations**: Implement the `Reporter` interface
2. **Error Handling**: Handle errors gracefully without disrupting runtime
3. **Thread Safety**: Ensure implementations are safe for concurrent use
4. **Testing**: Add comprehensive unit tests
5. **Documentation**: Update this README with new implementations

## License

See [LICENSE](../../../LICENSE) in the repository root.

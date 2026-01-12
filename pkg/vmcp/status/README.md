# Status Reporting Package

The `status` package provides platform-agnostic status reporting for vMCP runtime.

## Overview

This package enables vMCP runtime to report operational status to different destinations based on the deployment environment:

- **Kubernetes**: Updates VirtualMCPServer/status subresource via K8sReporter
- **CLI/Local**: Uses NoOpReporter (no status persistence needed)

## Architecture

### Reporter Interface

```go
type Reporter interface {
    // ReportStatus updates the complete status atomically
    ReportStatus(ctx context.Context, status *vmcptypes.Status) error

    // Start initializes the reporter and returns a shutdown function
    Start(ctx context.Context) (shutdown func(context.Context) error, err error)
}
```

### Status Model

The `vmcptypes.Status` structure captures complete vMCP operational state:

```go
type Status struct {
    Phase              Phase               // Ready, Degraded, Failed, Pending
    Message            string              // Human-readable message
    Conditions         []Condition         // Fine-grained status conditions
    DiscoveredBackends []DiscoveredBackend // Backend server information
    ObservedGeneration int64               // For optimistic concurrency
    Timestamp          time.Time           // When status was captured
}
```

## Implementations

### K8sReporter

Updates VirtualMCPServer/status subresource in Kubernetes clusters.

**Features:**
- Automatic environment detection via `VMCP_NAME` and `VMCP_NAMESPACE` env vars
- Uses in-cluster config for authentication
- Updates status subresource atomically
- Requires RBAC: `virtualmcpservers/status` get, update, patch

**Phase Conversion:**
- `vmcptypes.PhaseReady` → `VirtualMCPServerPhaseReady`
- `vmcptypes.PhaseDegraded` → `VirtualMCPServerPhaseDegraded`
- `vmcptypes.PhaseFailed` → `VirtualMCPServerPhaseFailed`
- `vmcptypes.PhasePending` → `VirtualMCPServerPhasePending`

### NoOpReporter

No-operation implementation for CLI/local environments where status persistence is not needed.

**Characteristics:**
- All methods return immediately without error
- No side effects or I/O
- Safe for concurrent use
- Zero overhead

### LoggingReporter

Logs status updates at debug level for CLI mode.

**Usage:**
- Not used by default (factory creates NoOpReporter for CLI)
- Available for explicit configuration if needed
- Logs phase, message, backend count, and timestamp

## Factory Pattern

The `NewReporter()` factory automatically selects the appropriate reporter:

```go
// Kubernetes mode (VMCP_NAME + VMCP_NAMESPACE set) → K8sReporter
// CLI mode (env vars not set) → NoOpReporter
reporter, err := status.NewReporter()
```

## Integration

### vMCP Server Integration

```go
// Create reporter (automatic environment detection)
statusReporter, err := status.NewReporter()
if err != nil {
    return fmt.Errorf("failed to create status reporter: %w", err)
}

// Initialize reporter
shutdown, err := statusReporter.Start(ctx)
if err != nil {
    return fmt.Errorf("failed to start status reporter: %w", err)
}
defer shutdown(ctx)

// Report status on events
status := &vmcptypes.Status{
    Phase:   vmcptypes.PhaseReady,
    Message: "All backends healthy",
    DiscoveredBackends: backends,
    Timestamp: time.Now(),
}
if err := statusReporter.ReportStatus(ctx, status); err != nil {
    logger.Errorf("failed to report status: %v", err)
}
```

### Lifecycle

1. **Initialization**: `NewReporter()` detects environment and creates appropriate reporter
2. **Start**: `Start(ctx)` initializes reporter and returns shutdown function
3. **Report**: `ReportStatus(ctx, status)` called when status changes
4. **Shutdown**: Call returned shutdown function during cleanup

## Testing

```bash
# Run all tests
go test ./pkg/vmcp/status/...

# Run with coverage
go test -cover ./pkg/vmcp/status/...

# Run with race detector
go test -race ./pkg/vmcp/status/...
```

## Related Documentation

- [Virtual MCP Proposal](../../../docs/proposals/THV-2106-virtual-mcp-server.md)
- [vMCP Package Overview](../doc.go)
- [GitHub Issue #3149](https://github.com/stacklok/toolhive/issues/3149)

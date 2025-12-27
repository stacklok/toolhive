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
                  │ LoggingReporter              │ │
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

### LoggingReporter

CLI-mode implementation that optionally logs status updates.

**Use when**: Running vMCP as a standalone CLI application.

**Characteristics**:
- No persistent status storage
- Optional debug logging (configurable via constructor)
- Minimal overhead when logging is disabled
- Thread-safe for concurrent status updates

**Example**:
```go
import vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"

// Create no-op reporter (optionally enable debug logging)
reporter := vmcpstatus.NewLoggingReporter(true) // true = log updates

// Start returns a shutdown function
shutdown, err := reporter.Start(ctx)
if err != nil {
    log.Fatal(err)
}
defer shutdown(ctx) // Cleanup on exit

// Report status updates
reporter.ReportStatus(ctx, status) // Logs but doesn't persist
```

### K8SReporter (Future Implementation)

Updates `VirtualMCPServer.Status` in Kubernetes cluster.

**Note**: K8SReporter implementation will be added in a follow-up PR. This current PR (#3147) includes only the interface and CLI-mode implementation (LoggingReporter).

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

### Phases

- **Pending**: Server is initializing, backend discovery in progress
- **Ready**: Server is healthy with at least one available backend
- **Degraded**: Some backends unavailable, but server still serving
- **Failed**: Server failed to start or all backends unavailable

### Conditions

Standard conditions that should be reported:

- **BackendsDiscovered**: Whether backend discovery completed successfully
- **Ready**: Whether the server is ready to serve requests
- **AuthConfigured**: Whether authentication is properly configured

Each condition has:
- `Type`: Condition identifier
- `Status`: True, False, or Unknown
- `Reason`: Programmatic identifier (e.g., `ServerReady`, `BackendDiscoveryFailed`)
- `Message`: Human-readable explanation
- `LastTransitionTime`: When the condition last changed

### DiscoveredBackends

Each backend includes:
- **Name**: Unique backend identifier
- **URL**: Backend endpoint
- **Status**: Ready, Degraded, Unavailable, or Unknown
- **AuthConfigRef**: Reference to auth configuration
- **AuthType**: Authentication method (e.g., oauth2, header_injection)
- **LastHealthCheck**: Timestamp of last health verification
- **Message**: Additional context about backend status

## Integration

StatusReporter is integrated into the vMCP server lifecycle:

### Server Configuration

```go
serverCfg := &vmcpserver.Config{
    Name:                "my-vmcp",
    Version:             "1.0.0",
    Host:                "127.0.0.1",
    Port:                4483,
    StatusReporter:      statusReporter,  // Can be nil to disable
    // ... other fields
}
```

### Server Lifecycle

The server automatically manages the StatusReporter lifecycle:

1. **Start**: Calls `statusReporter.Start(ctx)` which returns a shutdown function
2. **Runtime**: vMCP calls `ReportStatus()` as backends are discovered and health changes
3. **Stop**: Calls the shutdown function to flush pending updates and clean up

**Key Pattern**: The shutdown function returned by `Start()` makes cleanup more discoverable and harder to forget:

```go
// In server.Start()
if s.statusReporter != nil {
    shutdown, err := s.statusReporter.Start(ctx)
    if err != nil {
        logger.Warnw("failed to start status reporter", "error", err)
    } else {
        s.statusReporterShutdown = shutdown  // Store for later cleanup
    }
}

// In server.Stop()
if s.statusReporterShutdown != nil {
    if err := s.statusReporterShutdown(ctx); err != nil {
        logger.Errorw("failed to shutdown status reporter", "error", err)
    }
}
```

### Nil Reporter

If `StatusReporter` is nil, status reporting is completely disabled with zero overhead.

## Usage in CLI Mode

The vMCP CLI automatically creates a LoggingReporter with logging disabled:

```go
// Create logging status reporter for CLI mode
// Logging is disabled (false) for production CLI usage (no persistent status)
statusReporter := vmcpstatus.NewLoggingReporter(false)

serverCfg := &vmcpserver.Config{
    StatusReporter: statusReporter,
    // ... other fields
}
```

If you enable logging (`NewLoggingReporter(true)`), status updates will be logged:

```
DEBUG status update (not persisted in CLI mode) phase=Ready message="Server is ready" backend_count=2
```

## Thread Safety

All Reporter implementations must be thread-safe for concurrent calls to:
- `ReportStatus()` - Can be called from multiple goroutines
- `Start()` - Should be safe to call once (though typically called only once)
- Returned shutdown function - Should be idempotent (safe to call multiple times)

LoggingReporter is fully thread-safe with no shared mutable state. The shutdown function returned by `Start()` can be safely called multiple times (idempotent).

## Error Handling

StatusReporter methods return errors, but implementations should be robust:

- **LoggingReporter**: Never returns errors (always succeeds), shutdown function is also error-free and idempotent
- **K8SReporter** (future): May return transient errors (network, API server issues)
  - `Start()` may fail - server should log but continue
  - Shutdown function should be resilient to errors
  - Periodic reporting should retry automatically

## Future Enhancements

This is phase 1 of the StatusReporter implementation. Future work includes:

1. **K8SReporter Implementation** (Follow-up PR)
   - Updates VirtualMCPServer.Status via Kubernetes API
   - Requires RBAC permissions for status subresource
   - Handles optimistic concurrency control
   - Periodic background reporting

2. **Dynamic Backend Discovery** (Issue #3004)
   - Remove duplicate discovery from operator
   - Operator reads status from vMCP instead of discovering
   - vMCP becomes source of truth for backend state

3. **File-based Reporter** (Future)
   - Persist status to local file for CLI mode
   - Enable `thv status` command to read vMCP state

4. **Metrics Reporter** (Future)
   - Export status as Prometheus metrics
   - Phase, backend count, health gauges

## Related Issues

- [#3147](https://github.com/stacklok/toolhive/issues/3147): StatusReporter abstraction (this PR)
- [#3004](https://github.com/stacklok/toolhive/issues/3004): Remove operator backend discovery in dynamic mode
- [#2854](https://github.com/stacklok/toolhive/issues/2854): Original issue for status reporter

## Development

### Running Tests

```bash
# Run all tests
task test

# Run only status package tests
go test ./pkg/vmcp/status/...

# Run with coverage
go test -cover ./pkg/vmcp/status/...
```

### Adding a New Reporter

To add a new Reporter implementation:

1. Create `pkg/vmcp/status/your_reporter.go`
2. Implement the `Reporter` interface
3. Ensure thread safety (especially for shutdown function)
4. Make shutdown function idempotent
5. Add comprehensive tests
6. Document usage in this README

Example:

```go
type MyReporter struct {
    // fields
}

func NewMyReporter() *MyReporter {
    return &MyReporter{}
}

func (r *MyReporter) ReportStatus(ctx context.Context, status *Status) error {
    // Implementation
    return nil
}

func (r *MyReporter) Start(ctx context.Context) (func(context.Context) error, error) {
    // Initialize background processes

    // Return shutdown function
    shutdown := func(ctx context.Context) error {
        // Cleanup logic (should be idempotent)
        return nil
    }

    return shutdown, nil
}

// Verify interface implementation
var _ Reporter = (*MyReporter)(nil)
```

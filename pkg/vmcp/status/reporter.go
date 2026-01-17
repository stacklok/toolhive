package status

import (
	"context"
	"time"
)

// Reporter is the interface for reporting vMCP runtime status.
// Implementations of this interface enable the vMCP runtime to communicate
// its operational status to the appropriate destination (e.g., Kubernetes status subresource,
// logs, metrics systems, etc.).
//
// The Reporter interface is platform-agnostic and can be implemented for different
// deployment environments:
//   - NoOpReporter: For CLI/local development (no status reporting)
//   - K8sReporter: For Kubernetes deployments (updates VirtualMCPServer/status)
//   - LogReporter: For logging-based status reporting
//   - MetricsReporter: For metrics-based status reporting
//
// Implementations should be non-blocking and handle errors gracefully without
// disrupting the vMCP runtime.
type Reporter interface {
	// Report sends a status update to the appropriate destination.
	// This method should be non-blocking and return quickly.
	//
	// Parameters:
	//   - ctx: Context for cancellation and timeout
	//   - status: The current runtime status to report
	//
	// Returns an error if the status update fails, but implementations should
	// handle errors gracefully (e.g., by logging) without disrupting the caller.
	Report(ctx context.Context, status *RuntimeStatus) error

	// Start begins periodic status reporting with the given interval.
	// The reporter will call the provided statusFunc at the specified interval
	// to retrieve the current status and report it.
	//
	// This method starts a background goroutine and returns immediately.
	// The goroutine continues until Stop() is called or the context is cancelled.
	//
	// Parameters:
	//   - ctx: Context for cancellation and timeout
	//   - interval: How often to report status (e.g., 30s)
	//   - statusFunc: Function to call to get the current status
	//
	// Returns an error if the reporter cannot be started (e.g., invalid configuration).
	Start(ctx context.Context, interval time.Duration, statusFunc func() *RuntimeStatus) error

	// Stop stops periodic status reporting and cleans up resources.
	// This method should block until the reporter has fully stopped.
	//
	// After Stop() is called, the reporter should not send any more status updates.
	Stop()
}

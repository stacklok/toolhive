package status

import (
	"context"
	"time"
)

// Reporter is platform-agnostic interface for reporting Virtual MCP server status
type Reporter interface {
	// Report sends a single status update
	Report(ctx context.Context, status *RuntimeStatus) error

	// Start begins periodic status reporting in a background goroutine
	Start(ctx context.Context, interval time.Duration) error

	// Stop gracefully stops the periodic reporter
	Stop()

	// SetStatusCallback sets the function to retrieve current server status
	SetStatusCallback(fn func() *RuntimeStatus)
}

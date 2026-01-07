package status

import (
	"context"

	"github.com/stacklok/toolhive/pkg/logger"
)

// LoggingReporter is a CLI-mode implementation of Reporter that logs status updates.
//
// In CLI mode, there is no persistent status storage or control plane to report to.
// This implementation logs status updates for debugging but does not persist them.
//
// Use this when running vMCP as a standalone CLI application.
type LoggingReporter struct {
	// logUpdates controls whether status updates are logged (useful for debugging)
	logUpdates bool
}

// NewLoggingReporter creates a new logging status reporter for CLI mode.
//
// Set logUpdates=true to log status updates for debugging purposes.
// Set logUpdates=false for silent operation (no logging overhead).
func NewLoggingReporter(logUpdates bool) *LoggingReporter {
	return &LoggingReporter{
		logUpdates: logUpdates,
	}
}

// ReportStatus logs the status update if logging is enabled, but does not persist it.
func (r *LoggingReporter) ReportStatus(_ context.Context, status *Status) error {
	if status == nil {
		return nil // Silently ignore nil status
	}

	if r.logUpdates {
		logger.Debugw("status update (not persisted in CLI mode)",
			"phase", status.Phase,
			"message", status.Message,
			"backend_count", len(status.DiscoveredBackends),
			"timestamp", status.Timestamp)
	}
	return nil
}

// Start initializes the reporter (no background processes in CLI mode).
// Returns a shutdown function for cleanup (also a no-op in CLI mode).
func (r *LoggingReporter) Start(_ context.Context) (func(context.Context) error, error) {
	if r.logUpdates {
		logger.Debug("status reporter: starting (CLI mode - logging only)")
	}

	// Return a shutdown function
	shutdown := func(_ context.Context) error {
		if r.logUpdates {
			logger.Debug("status reporter: stopping (CLI mode)")
		}
		return nil
	}

	return shutdown, nil
}

// Verify LoggingReporter implements Reporter interface
var _ Reporter = (*LoggingReporter)(nil)

package status

import (
	"context"

	"github.com/stacklok/toolhive/pkg/logger"
)

// NoOpReporter is a silent implementation of Reporter for CLI mode.
//
// In CLI mode, there is no persistent status storage or control plane to report to.
// This implementation logs status updates for debugging but does not persist them.
//
// Use this when running vMCP as a standalone CLI application.
type NoOpReporter struct {
	// logUpdates controls whether status updates are logged (useful for debugging)
	logUpdates bool
}

// NewNoOpReporter creates a new no-op status reporter for CLI mode.
//
// Set logUpdates=true to log status updates for debugging purposes.
func NewNoOpReporter(logUpdates bool) *NoOpReporter {
	return &NoOpReporter{
		logUpdates: logUpdates,
	}
}

// ReportStatus logs the status update if logging is enabled, but does not persist it.
func (r *NoOpReporter) ReportStatus(_ context.Context, status *Status) error {
	if r.logUpdates {
		logger.Debugw("status update (not persisted in CLI mode)",
			"phase", status.Phase,
			"message", status.Message,
			"backend_count", len(status.DiscoveredBackends),
			"timestamp", status.Timestamp)
	}
	return nil
}

// Start is a no-op in CLI mode (no periodic reporting needed).
func (r *NoOpReporter) Start(_ context.Context) error {
	if r.logUpdates {
		logger.Debug("status reporter: no-op in CLI mode (no persistent status)")
	}
	return nil
}

// Stop is a no-op in CLI mode (nothing to stop).
func (r *NoOpReporter) Stop(_ context.Context) error {
	if r.logUpdates {
		logger.Debug("status reporter: stopping (no-op in CLI mode)")
	}
	return nil
}

// Verify NoOpReporter implements Reporter interface
var _ Reporter = (*NoOpReporter)(nil)

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"

	"github.com/stacklok/toolhive/pkg/logger"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

// LoggingReporter is a CLI-mode implementation of Reporter that logs status updates.
// In CLI mode there is no persistence; status updates are logged at Debug level.
// Debug logging is controlled by the --debug flag; logs may not be visible
// in production configurations where log level is set to Info.
type LoggingReporter struct{}

// NewLoggingReporter creates a logging status reporter for CLI mode.
func NewLoggingReporter() *LoggingReporter {
	return &LoggingReporter{}
}

// ReportStatus logs the status update (non-persistent).
func (*LoggingReporter) ReportStatus(_ context.Context, status *vmcptypes.Status) error {
	if status == nil {
		return nil
	}

	logger.Debugw("status update (not persisted in CLI mode)",
		"phase", status.Phase,
		"message", status.Message,
		"backend_count", len(status.DiscoveredBackends),
		"timestamp", status.Timestamp)
	return nil
}

// Start initializes the reporter (no background processes in CLI mode).
// Returns a shutdown function for cleanup (also a no-op in CLI mode).
func (*LoggingReporter) Start(_ context.Context) (func(context.Context) error, error) {
	logger.Debug("status reporter: starting (CLI mode - logging only)")

	shutdown := func(_ context.Context) error {
		logger.Debug("status reporter: stopping (CLI mode)")
		return nil
	}

	return shutdown, nil
}

// Verify LoggingReporter implements Reporter interface
var _ Reporter = (*LoggingReporter)(nil)

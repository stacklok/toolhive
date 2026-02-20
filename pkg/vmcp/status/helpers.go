// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"
	"log/slog"

	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

// shouldSkipStatus checks if status is nil and returns true if it should be skipped.
// Returns true when status is nil (invalid/should skip), false otherwise.
// This is a common validation used by all reporter implementations.
func shouldSkipStatus(status *vmcptypes.Status) bool {
	return status == nil
}

// noOpShutdown creates a no-op shutdown function with logging.
// Used by stateless reporters (LoggingReporter, K8sReporter) that don't need cleanup.
func noOpShutdown(mode string) func(context.Context) error {
	return func(_ context.Context) error {
		slog.Debug("status reporter: stopping", "mode", mode)
		return nil
	}
}

// logReporterStart logs reporter initialization at debug level.
func logReporterStart(mode, details string) {
	slog.Debug("status reporter: starting", "mode", mode, "details", details)
}

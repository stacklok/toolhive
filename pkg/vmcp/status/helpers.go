// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"

	"github.com/stacklok/toolhive/pkg/logger"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

// validateStatus checks if status is nil and returns true if it should be skipped.
// This is a common validation used by all reporter implementations.
func validateStatus(status *vmcptypes.Status) bool {
	return status == nil
}

// noOpShutdown creates a no-op shutdown function with logging.
// Used by stateless reporters (LoggingReporter, K8sReporter) that don't need cleanup.
func noOpShutdown(mode string) func(context.Context) error {
	return func(_ context.Context) error {
		logger.Debugf("status reporter: stopping (%s mode)", mode)
		return nil
	}
}

// logReporterStart logs reporter initialization at debug level.
func logReporterStart(mode, details string) {
	logger.Debugf("status reporter: starting (%s mode - %s)", mode, details)
}

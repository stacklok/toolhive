// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package status provides abstractions for vMCP runtime status reporting.
package status

import (
	"context"

	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

// NoOpReporter is a Reporter implementation that performs no operations.
// This is used in CLI/local development environments where status reporting
// is not needed or desired.
//
// All methods return immediately without error and perform no side effects.
// This implementation is safe for concurrent use.
type NoOpReporter struct{}

// NewNoOpReporter creates a new NoOpReporter instance.
// This is the default reporter for CLI/local development environments.
func NewNoOpReporter() *NoOpReporter {
	return &NoOpReporter{}
}

// ReportStatus does nothing and returns nil.
// This satisfies the Reporter interface for environments that don't need status reporting.
func (*NoOpReporter) ReportStatus(_ context.Context, _ *vmcptypes.Status) error {
	return nil
}

// Start does nothing and returns a no-op shutdown function.
// This satisfies the Reporter interface for environments that don't need status reporting.
func (*NoOpReporter) Start(_ context.Context) (func(context.Context) error, error) {
	shutdown := func(_ context.Context) error {
		return nil
	}
	return shutdown, nil
}

// Verify NoOpReporter implements Reporter interface
var _ Reporter = (*NoOpReporter)(nil)

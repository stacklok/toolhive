// Package status provides abstractions for vMCP runtime status reporting.
package status

import (
	"context"
	"time"
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

// Report does nothing and returns nil.
// This satisfies the Reporter interface for environments that don't need status reporting.
func (*NoOpReporter) Report(_ context.Context, _ *RuntimeStatus) error {
	return nil
}

// Start does nothing and returns nil.
// This satisfies the Reporter interface for environments that don't need status reporting.
func (*NoOpReporter) Start(_ context.Context, _ time.Duration, _ func() *RuntimeStatus) error {
	return nil
}

// Stop does nothing.
// This satisfies the Reporter interface for environments that don't need status reporting.
func (*NoOpReporter) Stop() {
	// No-op
}

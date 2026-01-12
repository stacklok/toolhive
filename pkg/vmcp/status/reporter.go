// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package status provides platform-agnostic status reporting for vMCP servers.
//
// The StatusReporter abstraction enables vMCP runtime to report operational status
// back to the control plane (Kubernetes operator or CLI state manager). This allows
// the runtime to autonomously update backend discovery results, health status, and
// operational state without relying on the controller to infer it through polling.
//
// This abstraction supports removing operator discovery in dynamic mode by allowing
// vMCP runtime to discover backends and report the results back.
package status

import (
	"context"

	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

// Reporter provides a platform-agnostic interface for vMCP runtime to report status.
//
// Implementations:
//   - K8sReporter: Updates VirtualMCPServer.Status in Kubernetes cluster (requires RBAC)
//   - LoggingReporter: Logs status at debug level for CLI mode (no persistent status)
//   - NoOpReporter: No-op implementation for CLI mode (no status reporting needed)
//
// The reporter is designed to be called by vMCP runtime during:
//   - Backend discovery (report discovered backends)
//   - Health checks (update backend health status)
//   - Lifecycle events (server starting, ready, degraded, failed)
type Reporter interface {
	// ReportStatus updates the complete status atomically.
	// This is the primary method for status reporting.
	ReportStatus(ctx context.Context, status *vmcptypes.Status) error

	// Start initializes the reporter.
	//
	// Returns:
	//   - shutdown: Function to stop the reporter and cleanup resources.
	//               Call this when shutting down (e.g., in server.Stop()).
	//               Blocks until all pending status updates are flushed.
	//               Safe to call multiple times (idempotent).
	//   - err:      Non-nil if initialization fails.
	//               When err != nil, shutdown will be nil.
	Start(ctx context.Context) (shutdown func(context.Context) error, err error)
}

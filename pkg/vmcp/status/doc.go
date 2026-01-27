// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package status provides platform-agnostic status reporting for vMCP runtime.
//
// The Reporter interface allows vMCP runtime to report operational status to different
// destinations based on deployment environment:
//
//   - K8sReporter: Updates VirtualMCPServer/status subresource (Kubernetes mode)
//   - NoOpReporter: No-op implementation for CLI mode (no persistence needed)
//   - LoggingReporter: Logs status at debug level (available but not used by factory)
//
// The factory pattern (NewReporter) automatically selects the appropriate reporter
// based on environment variables (VMCP_NAME + VMCP_NAMESPACE).
//
// Reporter lifecycle: Start(ctx) returns a shutdown func; server collects and
// calls shutdown funcs during Stop(). ReportStatus(ctx, *vmcp.Status) is
// thread-safe and handles nil status gracefully.
package status

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package status provides the StatusReporter interface for vMCP.
//
// The reporter allows vMCP runtime to publish its operational status using
// shared vmcp status types (pkg/vmcp/types.go). Implementations are pluggable:
//   - LoggingReporter (CLI): logs updates at Debug level, no persistence.
//     Debug logging is controlled by the --debug flag; logs may not be visible
//     in production configurations where log level is set to Info.
//   - Future reporters: Kubernetes status writer, file/metrics sinks.
//
// Reporter lifecycle: Start(ctx) returns a shutdown func; server collects and
// calls shutdown funcs during Stop(). ReportStatus(ctx, *vmcp.Status) is
// thread-safe and expected to be idempotent for repeated updates.
package status

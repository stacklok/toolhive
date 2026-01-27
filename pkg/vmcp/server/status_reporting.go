// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

// StatusReportingConfig configures periodic status reporting.
type StatusReportingConfig struct {
	// Interval is how often to report status.
	// Recommended: 30s.
	Interval time.Duration

	// Reporter is the status reporter to use.
	Reporter vmcpstatus.Reporter
}

// DefaultStatusReportingConfig returns sensible defaults.
func DefaultStatusReportingConfig() StatusReportingConfig {
	return StatusReportingConfig{
		Interval: 30 * time.Second,
	}
}

// periodicStatusReporting runs in a background goroutine and periodically reports
// vMCP runtime status to the configured reporter (K8s API or CLI logging).
//
// It pulls health information from the health monitor and converts it to vmcp.Status
// format, then sends it to the reporter.
//
// The goroutine runs until the context is cancelled or an unrecoverable error occurs.
func (s *Server) periodicStatusReporting(ctx context.Context, config StatusReportingConfig) {
	if config.Reporter == nil {
		logger.Debug("Status reporting disabled (no reporter configured)")
		return
	}

	logger.Infof("Starting periodic status reporting (interval: %v)", config.Interval)

	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()

	// Report status immediately on startup
	s.reportStatus(ctx, config.Reporter)

	for {
		select {
		case <-ctx.Done():
			logger.Debug("Status reporting stopped (context cancelled)")
			return

		case <-ticker.C:
			s.reportStatus(ctx, config.Reporter)
		}
	}
}

// reportStatus collects current runtime status and sends it to the reporter.
func (s *Server) reportStatus(ctx context.Context, reporter vmcpstatus.Reporter) {
	// Build status from health monitor if available
	var status *vmcp.Status

	s.healthMonitorMu.RLock()
	if s.healthMonitor != nil {
		status = s.healthMonitor.BuildStatus()
	} else {
		// No health monitor - create minimal status
		status = &vmcp.Status{
			Phase:     vmcp.PhaseReady,
			Message:   "Health monitoring disabled",
			Timestamp: time.Now(),
		}
	}
	s.healthMonitorMu.RUnlock()

	// Report status
	if err := reporter.ReportStatus(ctx, status); err != nil {
		logger.Errorf("Failed to report status: %v", err)
	}
}

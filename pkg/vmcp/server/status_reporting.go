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
// format, then sends it to the reporter. Reporting errors are logged but do not stop
// the goroutine - status reporting continues with a best-effort approach.
//
// The goroutine runs until the context is cancelled.
func (s *Server) periodicStatusReporting(ctx context.Context, config StatusReportingConfig) {
	if config.Reporter == nil {
		logger.Debug("Status reporting disabled (no reporter configured)")
		return
	}

	// Validate interval to prevent panic from time.NewTicker
	interval := config.Interval
	if interval <= 0 {
		logger.Warnf("Invalid status reporting interval %v, defaulting to 30s", interval)
		interval = 30 * time.Second
	}

	logger.Infof("Starting periodic status reporting (interval: %v)", interval)

	// Wait for initial health checks to complete before first status report
	// This ensures that the first status report has accurate health information
	// rather than reporting with backendCount=0 before checks complete
	s.healthMonitorMu.RLock()
	if s.healthMonitor != nil {
		logger.Debug("Waiting for initial health checks to complete before first status report")
		s.healthMonitorMu.RUnlock()
		s.healthMonitor.WaitForInitialHealthChecks()
		logger.Debug("Initial health checks complete, proceeding with status reporting")
	} else {
		s.healthMonitorMu.RUnlock()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Report status immediately after initial health checks complete
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

	// Log status at debug level
	logger.Debugf("Reporting status: phase=%s, backendCount=%d, discoveredBackends=%d",
		status.Phase, status.BackendCount, len(status.DiscoveredBackends))

	// Report status
	if err := reporter.ReportStatus(ctx, status); err != nil {
		logger.Errorf("Failed to report status: %v", err)
	}
}

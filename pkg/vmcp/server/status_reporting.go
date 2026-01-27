// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

const (
	// statusReportInterval is how often to report status
	statusReportInterval = 30 * time.Second

	// statusReportTimeout is the maximum time to wait for a status report
	statusReportTimeout = 5 * time.Second
)

// periodicStatusReporting runs in the background and periodically reports server status.
// It reports status every 30 seconds, sending updates to the configured StatusReporter.
// The goroutine exits when the context is cancelled.
func (s *Server) periodicStatusReporting(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(statusReportInterval)
	defer ticker.Stop()

	logger.Debugw("status reporting goroutine started",
		"interval", statusReportInterval)

	// Report initial status immediately
	s.reportStatus(ctx)

	for {
		select {
		case <-ctx.Done():
			logger.Debug("status reporting goroutine stopping")
			return
		case <-ticker.C:
			s.reportStatus(ctx)
		}
	}
}

// reportStatus builds current status and reports it via the StatusReporter.
// This method is called periodically by the periodicStatusReporting goroutine.
func (s *Server) reportStatus(parentCtx context.Context) {
	// Create timeout context for status reporting
	ctx, cancel := context.WithTimeout(parentCtx, statusReportTimeout)
	defer cancel()

	// Build current status from server state
	status := s.buildCurrentStatus(ctx)

	// Report status
	if err := s.statusReporter.ReportStatus(ctx, status); err != nil {
		logger.Warnw("failed to report status",
			"error", err,
			"phase", status.Phase)
	} else {
		logger.Debugw("status reported successfully",
			"phase", status.Phase,
			"backend_count", len(status.DiscoveredBackends))
	}
}

// buildCurrentStatus constructs a Status from the current server state.
// It queries the backend registry and health monitor to build a complete status snapshot.
func (s *Server) buildCurrentStatus(ctx context.Context) *vmcp.Status {
	// Get backends from registry
	backends := s.backendRegistry.List(ctx)

	// Get health summary (will be zero-valued if health monitoring disabled)
	healthSummary := s.GetHealthSummary()

	// Determine server phase based on health state
	phase := s.determinePhase(healthSummary, len(backends))

	// Build discovered backends list with health information
	// This includes real-time health monitor status when available
	discoveredBackends := s.buildDiscoveredBackends(backends)

	// Count healthy/ready backends from discovered backends list
	// This reflects health monitor status when available, otherwise uses K8s phase
	readyCount := 0
	for _, db := range discoveredBackends {
		if db.Status == "ready" {
			readyCount++
		}
	}

	// Build status conditions
	conditions := s.buildConditions(phase, healthSummary, len(backends))

	return &vmcp.Status{
		Phase:              phase,
		Message:            s.buildMessage(phase, healthSummary, len(backends)),
		DiscoveredBackends: discoveredBackends,
		BackendCount:       readyCount,
		Conditions:         conditions,
		Timestamp:          time.Now(),
	}
}

// determinePhase determines the server phase based on backend health.
// Phase progression: Pending → Ready → Degraded → Failed
func (*Server) determinePhase(summary health.Summary, totalBackends int) vmcp.Phase {
	// No backends discovered yet
	if totalBackends == 0 {
		return vmcp.PhasePending
	}

	// All backends unhealthy
	if summary.Unhealthy == totalBackends {
		return vmcp.PhaseFailed
	}

	// Some backends unhealthy or degraded
	if summary.Unhealthy > 0 || summary.Degraded > 0 {
		return vmcp.PhaseDegraded
	}

	// All backends healthy
	return vmcp.PhaseReady
}

// buildDiscoveredBackends converts backends to DiscoveredBackend format with health info.
func (s *Server) buildDiscoveredBackends(backends []vmcp.Backend) []vmcp.DiscoveredBackend {
	if len(backends) == 0 {
		return nil
	}

	discovered := make([]vmcp.DiscoveredBackend, 0, len(backends))
	for _, backend := range backends {
		// Get health state if monitoring is enabled
		var healthState *health.State
		if state, err := s.GetBackendHealthState(backend.ID); err == nil {
			healthState = state
		}

		// Determine status: prefer health monitor's status if available,
		// otherwise use the backend's initial status from K8s discovery
		status := backend.HealthStatus
		if healthState != nil {
			// Health monitor has run checks, use its status
			status = healthState.Status
		}

		db := vmcp.DiscoveredBackend{
			Name:   backend.Name,
			URL:    backend.BaseURL,
			Status: status.ToCRDStatus(),
		}

		// Add auth info if configured
		if backend.AuthConfig != nil {
			db.AuthType = backend.AuthConfig.Type
			// Expose auth config reference name for debugging and visibility
			db.AuthConfigRef = backend.AuthConfigRef
		}

		// Add health check timestamp and error message if available
		if healthState != nil {
			db.LastHealthCheck = metav1.Time{Time: healthState.LastCheckTime}
			// Add error message if health check failed
			if healthState.LastError != nil {
				db.Message = healthState.LastError.Error()
			}
		}

		discovered = append(discovered, db)
	}

	return discovered
}

// buildConditions creates status conditions based on server state.
func (*Server) buildConditions(phase vmcp.Phase, _ health.Summary, totalBackends int) []vmcp.Condition {
	now := metav1.Now()
	var conditions []vmcp.Condition

	// BackendsDiscovered condition
	if totalBackends > 0 {
		conditions = append(conditions, vmcp.Condition{
			Type:               vmcp.ConditionTypeBackendsDiscovered,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             vmcp.ReasonBackendDiscoverySucceeded,
			Message:            "Backends successfully discovered",
		})
	} else {
		conditions = append(conditions, vmcp.Condition{
			Type:               vmcp.ConditionTypeBackendsDiscovered,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             vmcp.ReasonBackendDiscoveryFailed,
			Message:            "No backends discovered",
		})
	}

	// Ready condition
	var readyCondition vmcp.Condition
	switch phase {
	case vmcp.PhaseReady:
		readyCondition = vmcp.Condition{
			Type:               vmcp.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             vmcp.ReasonServerReady,
			Message:            "Server is ready and all backends are healthy",
		}
	case vmcp.PhaseDegraded:
		readyCondition = vmcp.Condition{
			Type:               vmcp.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             vmcp.ReasonServerDegraded,
			Message:            "Server is operational but some backends are unhealthy or degraded",
		}
	case vmcp.PhaseFailed:
		readyCondition = vmcp.Condition{
			Type:               vmcp.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             vmcp.ReasonServerFailed,
			Message:            "Server failed: all backends are unhealthy",
		}
	case vmcp.PhasePending:
		readyCondition = vmcp.Condition{
			Type:               vmcp.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			LastTransitionTime: now,
			Reason:             vmcp.ReasonServerStarting,
			Message:            "Server is starting, waiting for backend discovery",
		}
	}
	conditions = append(conditions, readyCondition)

	return conditions
}

// buildMessage creates a human-readable status message based on server state.
func (*Server) buildMessage(phase vmcp.Phase, _ health.Summary, totalBackends int) string {
	if totalBackends == 0 {
		return "Waiting for backend discovery"
	}

	switch phase {
	case vmcp.PhaseReady:
		return "All backends healthy"
	case vmcp.PhaseDegraded:
		return "Some backends unhealthy or degraded"
	case vmcp.PhaseFailed:
		return "All backends unhealthy"
	case vmcp.PhasePending:
		return "Starting up"
	default:
		return "Unknown status"
	}
}

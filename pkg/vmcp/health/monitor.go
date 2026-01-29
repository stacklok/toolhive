// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// healthCheckContextKey is a marker for health check requests.
type healthCheckContextKey struct{}

// WithHealthCheckMarker marks a context as a health check request.
// Authentication layers can use IsHealthCheck to identify and skip authentication
// for health check requests.
func WithHealthCheckMarker(ctx context.Context) context.Context {
	return context.WithValue(ctx, healthCheckContextKey{}, true)
}

// IsHealthCheck returns true if the context is marked as a health check.
// Authentication strategies use this to bypass authentication for health checks,
// since health checks verify backend availability and should not require user credentials.
// Returns false for nil contexts.
func IsHealthCheck(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	val, ok := ctx.Value(healthCheckContextKey{}).(bool)
	return ok && val
}

// Monitor performs periodic health checks on backend MCP servers.
// It runs background goroutines for each backend, tracking their health status
// and consecutive failure counts. The monitor supports graceful shutdown and
// provides thread-safe access to backend health information.
type Monitor struct {
	// checker performs health checks on backends.
	checker vmcp.HealthChecker

	// statusTracker tracks health status for all backends.
	statusTracker *statusTracker

	// checkInterval is how often to perform health checks.
	checkInterval time.Duration

	// backends is the list of backends to monitor.
	backends []vmcp.Backend

	// ctx is the context for the monitor's lifecycle.
	ctx context.Context

	// cancel cancels all health check goroutines.
	cancel context.CancelFunc

	// wg tracks running health check goroutines.
	wg sync.WaitGroup

	// mu protects the started and stopped flags.
	mu sync.Mutex

	// started indicates if the monitor has been started.
	started bool

	// stopped indicates if the monitor has been stopped (cannot be restarted).
	stopped bool
}

// MonitorConfig contains configuration for the health monitor.
type MonitorConfig struct {
	// CheckInterval is how often to perform health checks.
	// Must be > 0. Recommended: 30s.
	CheckInterval time.Duration

	// UnhealthyThreshold is the number of consecutive failures before marking unhealthy.
	// Must be >= 1. Recommended: 3 failures.
	UnhealthyThreshold int

	// Timeout is the maximum duration for a single health check operation.
	// Zero means no timeout (not recommended).
	Timeout time.Duration

	// DegradedThreshold is the response time threshold for marking a backend as degraded.
	// If a health check succeeds but takes longer than this duration, the backend is marked degraded.
	// Zero means disabled (backends will never be marked degraded based on response time alone).
	// Recommended: 5s.
	DegradedThreshold time.Duration
}

// DefaultConfig returns sensible default configuration values.
func DefaultConfig() MonitorConfig {
	return MonitorConfig{
		CheckInterval:      30 * time.Second,
		UnhealthyThreshold: 3,
		Timeout:            10 * time.Second,
		DegradedThreshold:  5 * time.Second,
	}
}

// NewMonitor creates a new health monitor for the given backends.
//
// Parameters:
//   - client: BackendClient for communicating with backend MCP servers
//   - backends: List of backends to monitor
//   - config: Configuration for health monitoring
//   - selfURL: Optional server's own URL. If provided, health checks targeting this URL are short-circuited.
//
// Returns (monitor, error). Error is returned if configuration is invalid.
func NewMonitor(
	client vmcp.BackendClient,
	backends []vmcp.Backend,
	config MonitorConfig,
	selfURL string,
) (*Monitor, error) {
	// Validate configuration
	if config.CheckInterval <= 0 {
		return nil, fmt.Errorf("check interval must be > 0, got %v", config.CheckInterval)
	}
	if config.UnhealthyThreshold < 1 {
		return nil, fmt.Errorf("unhealthy threshold must be >= 1, got %d", config.UnhealthyThreshold)
	}

	// Create health checker with degraded threshold and self URL
	checker := NewHealthChecker(client, config.Timeout, config.DegradedThreshold, selfURL)

	// Create status tracker
	statusTracker := newStatusTracker(config.UnhealthyThreshold)

	return &Monitor{
		checker:       checker,
		statusTracker: statusTracker,
		checkInterval: config.CheckInterval,
		backends:      backends,
	}, nil
}

// Start begins health monitoring for all backends.
// This spawns a background goroutine for each backend that performs periodic health checks.
// Returns an error if the monitor is already started, has been stopped, or if the parent context is invalid.
//
// The monitor respects the parent context for cancellation. When the parent context is
// cancelled, all health check goroutines will stop gracefully.
//
// Note: A monitor cannot be restarted after it has been stopped. Create a new monitor instead.
func (m *Monitor) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return fmt.Errorf("monitor has been stopped and cannot be restarted")
	}

	if m.started {
		return fmt.Errorf("monitor already started")
	}

	if ctx == nil {
		return fmt.Errorf("context cannot be nil")
	}

	// Create monitor context with cancellation
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true

	logger.Infof("Starting health monitor for %d backends (interval: %v, threshold: %d)",
		len(m.backends), m.checkInterval, m.statusTracker.unhealthyThreshold)

	// Start health check goroutine for each backend
	for i := range m.backends {
		backend := &m.backends[i] // Capture backend pointer for this iteration
		m.wg.Add(1)
		go m.monitorBackend(m.ctx, backend)
	}

	return nil
}

// Stop gracefully stops health monitoring.
// This cancels all health check goroutines and waits for them to complete.
// Returns an error if the monitor was not started.
//
// After stopping, the monitor cannot be restarted. Create a new monitor if needed.
func (m *Monitor) Stop() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return fmt.Errorf("monitor not started")
	}

	// Cancel all health check goroutines
	logger.Infof("Stopping health monitor for %d backends", len(m.backends))
	m.cancel()
	m.started = false
	m.stopped = true
	m.mu.Unlock()

	// Wait for all goroutines to complete
	m.wg.Wait()
	logger.Info("Health monitor stopped")

	return nil
}

// monitorBackend performs periodic health checks for a single backend.
// This runs in a background goroutine and continues until the context is cancelled.
func (m *Monitor) monitorBackend(ctx context.Context, backend *vmcp.Backend) {
	defer m.wg.Done()

	logger.Debugf("Starting health monitoring for backend %s", backend.Name)

	// Create ticker for periodic checks
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	// Perform initial health check immediately
	m.performHealthCheck(ctx, backend)

	// Periodic health check loop
	for {
		select {
		case <-ctx.Done():
			logger.Debugf("Stopping health monitoring for backend %s", backend.Name)
			return

		case <-ticker.C:
			m.performHealthCheck(ctx, backend)
		}
	}
}

// performHealthCheck performs a single health check for a backend and updates status.
func (m *Monitor) performHealthCheck(ctx context.Context, backend *vmcp.Backend) {
	// Create BackendTarget from Backend
	target := &vmcp.BackendTarget{
		WorkloadID:    backend.ID,
		WorkloadName:  backend.Name,
		BaseURL:       backend.BaseURL,
		TransportType: backend.TransportType,
		AuthConfig:    backend.AuthConfig,
		HealthStatus:  vmcp.BackendUnknown, // Status is determined by the health check
		Metadata:      backend.Metadata,
	}

	// Mark context as health check to bypass authentication
	// Health checks verify backend availability and should not require user credentials
	healthCheckCtx := WithHealthCheckMarker(ctx)

	// Perform health check
	status, err := m.checker.CheckHealth(healthCheckCtx, target)

	// Record result in status tracker
	if err != nil {
		m.statusTracker.RecordFailure(backend.ID, backend.Name, status, err)
	} else {
		// Pass status to RecordSuccess - it may be healthy or degraded (from slow response)
		// RecordSuccess will further check for recovering state (had recent failures)
		m.statusTracker.RecordSuccess(backend.ID, backend.Name, status)
	}
}

// GetBackendStatus returns the current health status for a backend.
// Returns (status, error). Error is returned if the backend is not being monitored.
func (m *Monitor) GetBackendStatus(backendID string) (vmcp.BackendHealthStatus, error) {
	status, exists := m.statusTracker.GetStatus(backendID)
	if !exists {
		return vmcp.BackendUnknown, fmt.Errorf("backend %s not found", backendID)
	}
	return status, nil
}

// GetBackendState returns the full health state for a backend.
// Returns (state, error). Error is returned if the backend is not being monitored.
func (m *Monitor) GetBackendState(backendID string) (*State, error) {
	state, exists := m.statusTracker.GetState(backendID)
	if !exists {
		return nil, fmt.Errorf("backend %s not found", backendID)
	}
	return state, nil
}

// GetAllBackendStates returns health states for all monitored backends.
// Returns a map of backend ID to State.
func (m *Monitor) GetAllBackendStates() map[string]*State {
	return m.statusTracker.GetAllStates()
}

// IsBackendHealthy returns true if the backend is currently healthy.
// Returns false if the backend is not being monitored or is unhealthy.
func (m *Monitor) IsBackendHealthy(backendID string) bool {
	return m.statusTracker.IsHealthy(backendID)
}

// GetHealthSummary returns a summary of backend health for logging/monitoring.
// Returns counts of healthy, degraded, unhealthy, and total backends.
func (m *Monitor) GetHealthSummary() Summary {
	allStates := m.statusTracker.GetAllStates()
	return computeSummary(allStates)
}

// computeSummary computes a Summary from a snapshot of backend states.
// This is a pure function that takes a states map and returns aggregated counts.
func computeSummary(allStates map[string]*State) Summary {
	summary := Summary{
		Total:           len(allStates),
		Healthy:         0,
		Degraded:        0,
		Unhealthy:       0,
		Unknown:         0,
		Unauthenticated: 0,
	}

	for _, state := range allStates {
		switch state.Status {
		case vmcp.BackendHealthy:
			summary.Healthy++
		case vmcp.BackendDegraded:
			summary.Degraded++
		case vmcp.BackendUnhealthy:
			summary.Unhealthy++
		case vmcp.BackendUnknown:
			summary.Unknown++
		case vmcp.BackendUnauthenticated:
			summary.Unauthenticated++
		}
	}

	return summary
}

// Summary provides aggregate health statistics for all backends.
type Summary struct {
	Total           int
	Healthy         int
	Degraded        int
	Unhealthy       int
	Unknown         int
	Unauthenticated int
}

// String returns a human-readable summary.
func (s Summary) String() string {
	return fmt.Sprintf("total=%d healthy=%d degraded=%d unhealthy=%d unknown=%d unauthenticated=%d",
		s.Total, s.Healthy, s.Degraded, s.Unhealthy, s.Unknown, s.Unauthenticated)
}

// BuildStatus builds a vmcp.Status from the current health monitor state.
// This converts backend health information into the format needed for status reporting
// to the Kubernetes API or CLI output.
//
// Phase determination:
// - Ready: All backends healthy, or no backends configured (cold start)
// - Pending: Backends configured but no health check data yet (waiting for first check)
// - Degraded: Some backends healthy, some degraded/unhealthy
// - Failed: No healthy backends (and at least one backend exists)
//
// Returns a Status instance with current health information and discovered backends.
//
// Takes a single snapshot of backend states to ensure internal consistency under
// concurrent updates.
func (m *Monitor) BuildStatus() *vmcp.Status {
	// Take a single snapshot of all backend states
	// This ensures consistency between summary counts and discovered backends
	allStates := m.GetAllBackendStates()

	// Compute summary from the snapshot (not a separate query)
	summary := computeSummary(allStates)

	// Pass configured backend count to distinguish between:
	// - No backends configured (cold start) vs
	// - Backends configured but no health data yet (waiting for first check)
	configuredBackendCount := len(m.backends)
	phase := determinePhase(summary, configuredBackendCount)
	message := formatStatusMessage(summary, phase, configuredBackendCount)
	discoveredBackends := m.convertToDiscoveredBackends(allStates)
	conditions := buildConditions(summary, phase, configuredBackendCount)

	return &vmcp.Status{
		Phase:              phase,
		Message:            message,
		Conditions:         conditions,
		DiscoveredBackends: discoveredBackends,
		BackendCount:       summary.Healthy, // Only count healthy backends
		Timestamp:          time.Now(),
	}
}

// determinePhase determines the overall phase based on backend health.
// Takes both the health summary and the count of configured backends to distinguish:
// - No backends configured (configuredCount==0): Ready (cold start)
// - Backends configured but no health data (configuredCount>0 && summary.Total==0): Pending
// - Has health data: Ready/Degraded/Failed based on health status
func determinePhase(summary Summary, configuredBackendCount int) vmcp.Phase {
	if summary.Total == 0 {
		// No health data yet - distinguish cold start from waiting for first check
		if configuredBackendCount == 0 {
			return vmcp.PhaseReady // True cold start - no backends configured
		}
		return vmcp.PhasePending // Backends configured but health checks not complete
	}
	if summary.Healthy == summary.Total {
		return vmcp.PhaseReady
	}
	if summary.Healthy == 0 {
		return vmcp.PhaseFailed
	}
	return vmcp.PhaseDegraded
}

// formatStatusMessage creates a human-readable message describing overall status.
func formatStatusMessage(summary Summary, phase vmcp.Phase, configuredBackendCount int) string {
	if summary.Total == 0 {
		// No health data yet - distinguish cold start from waiting for checks
		if configuredBackendCount == 0 {
			return "Ready, no backends configured"
		}
		return fmt.Sprintf("Waiting for initial health checks (%d backends configured)", configuredBackendCount)
	}
	if phase == vmcp.PhaseReady {
		return fmt.Sprintf("All %d backends healthy", summary.Healthy)
	}

	// Format unhealthy backend counts (shared by Failed and Degraded)
	unhealthyDetails := fmt.Sprintf("%d degraded, %d unhealthy, %d unknown, %d unauthenticated",
		summary.Degraded, summary.Unhealthy, summary.Unknown, summary.Unauthenticated)

	if phase == vmcp.PhaseFailed {
		return fmt.Sprintf("No healthy backends (%s)", unhealthyDetails)
	}
	// Degraded
	return fmt.Sprintf("%d/%d backends healthy (%s)", summary.Healthy, summary.Total, unhealthyDetails)
}

// convertToDiscoveredBackends converts backend health states to DiscoveredBackend format.
func (m *Monitor) convertToDiscoveredBackends(allStates map[string]*State) []vmcp.DiscoveredBackend {
	discoveredBackends := make([]vmcp.DiscoveredBackend, 0, len(allStates))

	for _, backend := range m.backends {
		state, exists := allStates[backend.ID]
		if !exists {
			continue // Skip backends not yet tracked (shouldn't happen)
		}

		authConfigRef, authType := extractAuthInfo(backend)

		discoveredBackends = append(discoveredBackends, vmcp.DiscoveredBackend{
			Name:            backend.Name,
			URL:             backend.BaseURL,
			Status:          state.Status.ToCRDStatus(),
			AuthConfigRef:   authConfigRef,
			AuthType:        authType,
			LastHealthCheck: metav1.NewTime(state.LastCheckTime),
			Message:         formatBackendMessage(state),
		})
	}

	return discoveredBackends
}

// extractAuthInfo extracts authentication information from a backend.
// Returns the AuthConfigRef (if populated during discovery) and the auth type.
func extractAuthInfo(backend vmcp.Backend) (authConfigRef, authType string) {
	if backend.AuthConfig == nil {
		return "", ""
	}
	// Use the actual AuthConfigRef populated during backend discovery.
	// In K8s mode, this is the name of the MCPExternalAuthConfig resource.
	// In CLI mode or when not discovered via K8s, this may be empty.
	return backend.AuthConfigRef, backend.AuthConfig.Type
}

// formatBackendMessage creates a human-readable message for a backend's health state.
func formatBackendMessage(state *State) string {
	if state.LastError != nil {
		return fmt.Sprintf("%s (failures: %d)", state.LastError.Error(), state.ConsecutiveFailures)
	}

	switch state.Status {
	case vmcp.BackendHealthy:
		return "Healthy"
	case vmcp.BackendDegraded:
		if state.ConsecutiveFailures > 0 {
			return fmt.Sprintf("Recovering from %d failures", state.ConsecutiveFailures)
		}
		return "Degraded performance"
	case vmcp.BackendUnhealthy:
		return "Unhealthy"
	case vmcp.BackendUnauthenticated:
		return "Authentication required"
	case vmcp.BackendUnknown:
		return "Unknown"
	default:
		return string(state.Status)
	}
}

// buildConditions creates Kubernetes-style conditions based on health summary and phase.
// Takes configured backend count to properly distinguish cold start from pending health checks.
func buildConditions(summary Summary, phase vmcp.Phase, configuredBackendCount int) []metav1.Condition {
	now := metav1.Now()
	conditions := []metav1.Condition{}

	// Ready condition - true if phase is Ready
	readyCondition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		LastTransitionTime: now,
		Reason:             "BackendsUnhealthy",
		Message:            "Not all backends are healthy",
	}

	switch phase {
	case vmcp.PhaseReady:
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "AllBackendsHealthy"
		// Distinguish cold start (no backends configured) from having healthy backends
		if summary.Total == 0 && configuredBackendCount == 0 {
			readyCondition.Message = "Ready, no backends configured"
		} else {
			readyCondition.Message = fmt.Sprintf("All %d backends are healthy", summary.Healthy)
		}
	case vmcp.PhaseDegraded:
		readyCondition.Reason = "SomeBackendsUnhealthy"
		readyCondition.Message = fmt.Sprintf("%d/%d backends healthy", summary.Healthy, summary.Total)
	case vmcp.PhaseFailed:
		readyCondition.Reason = "NoHealthyBackends"
		readyCondition.Message = "No healthy backends available"
	case vmcp.PhasePending:
		readyCondition.Reason = "BackendsPending"
		readyCondition.Message = fmt.Sprintf("Waiting for initial health checks (%d backends configured)", configuredBackendCount)
	default:
		// Unknown phase - use default values set above
		readyCondition.Reason = "BackendsUnhealthy"
		readyCondition.Message = "Backend status unknown"
	}

	conditions = append(conditions, readyCondition)

	// Degraded condition - true if any backends are degraded
	if summary.Degraded > 0 {
		conditions = append(conditions, metav1.Condition{
			Type:               "Degraded",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "BackendsDegraded",
			Message:            fmt.Sprintf("%d backends degraded", summary.Degraded),
		})
	}

	return conditions
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/toolhive/pkg/vmcp"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// WithHealthCheckMarker marks a context as a health check request.
// Authentication layers can use IsHealthCheck to identify and skip authentication
// for health check requests.
func WithHealthCheckMarker(ctx context.Context) context.Context {
	return healthcontext.WithHealthCheckMarker(ctx)
}

// IsHealthCheck returns true if the context is marked as a health check.
// Authentication strategies use this to bypass authentication for health checks,
// since health checks verify backend availability and should not require user credentials.
// Returns false for nil contexts.
func IsHealthCheck(ctx context.Context) bool {
	return healthcontext.IsHealthCheck(ctx)
}

// StatusProvider provides read-only access to backend health status.
// This interface enables discovery middleware to query current health state
// without depending on the full Monitor implementation or internal state.
//
// The interface is satisfied by Monitor when health monitoring is enabled,
// and can be nil when health monitoring is disabled (discovery falls back
// to registry's initial health status).
type StatusProvider interface {
	// QueryBackendStatus returns the current health status for a backend.
	// Returns (status, exists) where exists indicates if the backend is being monitored.
	// If exists is false, the caller should fall back to the backend registry's status.
	//
	// This method is safe for concurrent access and does not block on health checks.
	QueryBackendStatus(backendID string) (vmcp.BackendHealthStatus, bool)
}

// backendCheck manages the health check goroutine lifecycle for a single backend.
// It owns the backend snapshot and the cancel function for its goroutine, keeping
// per-backend lifecycle mechanics out of the Monitor's coordination logic.
//
// Thread-safety: backendCheck is NOT independently thread-safe. All calls must be
// made while holding the Monitor's locks — see start() and stop() for details.
type backendCheck struct {
	backend vmcp.Backend
	cancel  context.CancelFunc
}

// start begins the health check goroutine for this backend.
// The monitor's wg is incremented before the goroutine launches.
// If isInitial is true, the monitor's initialCheckWg is also incremented.
//
// Locking: the caller must hold both m.mu and m.backendsMu. m.mu prevents
// wg.Add() from racing with wg.Wait() in Stop().
func (bc *backendCheck) start(parentCtx context.Context, m *Monitor, isInitial bool) {
	ctx, cancel := context.WithCancel(parentCtx)
	bc.cancel = cancel
	m.wg.Add(1)
	if isInitial {
		m.initialCheckWg.Add(1)
	}
	go m.monitorBackend(ctx, &bc.backend, isInitial)
}

// stop cancels the health check goroutine for this backend.
// The goroutine will exit on its next context check and call wg.Done().
//
// Locking: the caller must hold m.backendsMu.
func (bc *backendCheck) stop() {
	if bc.cancel != nil {
		bc.cancel()
	}
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
	// Protected by backendsMu for thread-safe updates during backend changes.
	backends   []vmcp.Backend
	backendsMu sync.RWMutex

	// activeChecks maps backend IDs to their per-backend check lifecycle.
	// Each backendCheck owns the backend snapshot and cancel function for its goroutine.
	// Protected by backendsMu.
	activeChecks map[string]*backendCheck

	// ctx is the context for the monitor's lifecycle.
	ctx context.Context

	// cancel cancels all health check goroutines.
	cancel context.CancelFunc

	// wg tracks running health check goroutines.
	wg sync.WaitGroup

	// initialCheckWg tracks the initial health check for each backend.
	// This allows callers to wait for all initial health checks to complete
	// before relying on health status.
	initialCheckWg sync.WaitGroup

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

	// CircuitBreaker contains circuit breaker configuration.
	// nil means circuit breaker is disabled.
	CircuitBreaker *CircuitBreakerConfig
}

// CircuitBreakerConfig contains circuit breaker configuration.
type CircuitBreakerConfig struct {
	// Enabled controls whether circuit breaker is active.
	// +kubebuilder:default=false
	Enabled bool

	// FailureThreshold is the number of failures before opening the circuit.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=5
	// Must be >= 1. Recommended: 5 failures.
	FailureThreshold int

	// Timeout is the duration to wait in open state before attempting recovery.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern="^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$"
	// +kubebuilder:default="60s"
	// Recommended: 60s.
	Timeout time.Duration
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
//
// Returns (monitor, error). Error is returned if configuration is invalid.
func NewMonitor(
	client vmcp.BackendClient,
	backends []vmcp.Backend,
	config MonitorConfig,
) (*Monitor, error) {
	// Validate configuration
	if config.CheckInterval <= 0 {
		return nil, fmt.Errorf("check interval must be > 0, got %v", config.CheckInterval)
	}
	if config.UnhealthyThreshold < 1 {
		return nil, fmt.Errorf("unhealthy threshold must be >= 1, got %d", config.UnhealthyThreshold)
	}

	// Validate circuit breaker configuration if provided
	if config.CircuitBreaker != nil && config.CircuitBreaker.Enabled {
		if config.CircuitBreaker.FailureThreshold < 1 {
			return nil, fmt.Errorf("circuit breaker failure threshold must be >= 1, got %d", config.CircuitBreaker.FailureThreshold)
		}
		if config.CircuitBreaker.Timeout <= 0 {
			return nil, fmt.Errorf("circuit breaker timeout must be > 0, got %v", config.CircuitBreaker.Timeout)
		}
	}

	// Create health checker with degraded threshold
	checker := NewHealthChecker(client, config.Timeout, config.DegradedThreshold)

	// Create status tracker with circuit breaker configuration
	// The status tracker will lazily initialize circuit breakers as needed
	statusTracker := newStatusTracker(config.UnhealthyThreshold, config.CircuitBreaker)

	return &Monitor{
		checker:       checker,
		statusTracker: statusTracker,
		checkInterval: config.CheckInterval,
		backends:      backends,
		activeChecks:  make(map[string]*backendCheck),
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

	slog.Info("starting health monitor",
		"backends", len(m.backends),
		"interval", m.checkInterval,
		"threshold", m.statusTracker.unhealthyThreshold)

	// Start health check goroutine for each backend
	m.backendsMu.Lock()
	for _, b := range m.backends {
		bc := &backendCheck{backend: b}
		bc.start(m.ctx, m, true) // true = initial backend
		m.activeChecks[b.ID] = bc
	}
	m.backendsMu.Unlock()

	return nil
}

// WaitForInitialHealthChecks blocks until all backends have completed their initial health check.
// This is useful for ensuring that health status is accurate before relying on it (e.g., before
// reporting initial status to an external system).
//
// If the monitor was not started, this returns immediately (no initial checks to wait for).
// This method is safe to call multiple times and from multiple goroutines.
func (m *Monitor) WaitForInitialHealthChecks() {
	m.initialCheckWg.Wait()
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
	m.backendsMu.RLock()
	backendCount := len(m.backends)
	m.backendsMu.RUnlock()

	slog.Info("stopping health monitor", "backends", backendCount)
	m.cancel()
	m.started = false
	m.stopped = true
	m.mu.Unlock()

	// Wait for all goroutines to complete
	m.wg.Wait()
	slog.Info("health monitor stopped")

	return nil
}

// UpdateBackends updates the list of backends being monitored.
// Starts monitoring new backends and stops monitoring removed backends.
// This method is safe to call while the monitor is running.
func (m *Monitor) UpdateBackends(newBackends []vmcp.Backend) {
	// Hold m.mu throughout to prevent race with Stop()
	// This ensures m.wg.Add() cannot happen after Stop() calls m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started || m.stopped {
		return
	}

	m.backendsMu.Lock()
	defer m.backendsMu.Unlock()

	newBackendsMap := make(map[string]vmcp.Backend, len(newBackends))
	for _, b := range newBackends {
		newBackendsMap[b.ID] = b
	}

	// Update backends list before starting goroutines
	// This ensures GetHealthSummary sees new backends before their health checks complete
	m.backends = newBackends

	// Start monitoring for new or changed backends
	for id, backend := range newBackendsMap {
		if existing, ok := m.activeChecks[id]; ok {
			if !backendChanged(existing.backend, backend) {
				continue // Existing backend with no relevant changes
			}
			// Backend properties changed (e.g., URL updated after operator reconcile).
			// Stop the old goroutine so a new one starts with the updated properties.
			slog.Info("restarting health monitoring for changed backend",
				"backend", backend.Name, "old_url", existing.backend.BaseURL, "new_url", backend.BaseURL)
			existing.stop()
		} else {
			slog.Info("starting health monitoring for new backend", "backend", backend.Name)
		}

		bc := &backendCheck{backend: backend}
		// Clear the "removed" flag if this backend was previously removed
		// This allows health check results to be recorded again
		m.statusTracker.ClearRemovedFlag(id)
		bc.start(m.ctx, m, false) // false = dynamically added backend
		m.activeChecks[id] = bc
	}

	// Stop monitoring for removed backends and clean up their state
	for id, bc := range m.activeChecks {
		if _, exists := newBackendsMap[id]; !exists {
			slog.Info("stopping health monitoring for removed backend", "backend", bc.backend.Name)
			bc.stop()
			delete(m.activeChecks, id)
			// Remove backend from status tracker so it no longer appears in status reports
			m.statusTracker.RemoveBackend(id)
		}
	}
}

// monitorBackend performs periodic health checks for a single backend.
// This runs in a background goroutine and continues until the context is cancelled.
// The isInitial parameter indicates whether this is an initial backend (started in Start())
// or a dynamically added backend (added via UpdateBackends()). Only initial backends
// participate in the initialCheckWg synchronization.
func (m *Monitor) monitorBackend(ctx context.Context, backend *vmcp.Backend, isInitial bool) {
	defer m.wg.Done()

	slog.Debug("starting health monitoring for backend", "backend", backend.Name)

	// Create ticker for periodic checks
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	// Perform initial health check immediately
	m.performHealthCheck(ctx, backend)

	// Only signal completion for initial backends (started in Start()).
	// Dynamically added backends (via UpdateBackends) don't participate in
	// WaitForInitialHealthChecks() synchronization.
	if isInitial {
		m.initialCheckWg.Done() // Signal that initial check is complete
	}

	// Periodic health check loop
	for {
		select {
		case <-ctx.Done():
			slog.Debug("stopping health monitoring for backend", "backend", backend.Name)
			return

		case <-ticker.C:
			m.performHealthCheck(ctx, backend)
		}
	}
}

// performHealthCheck performs a single health check for a backend and updates status.
func (m *Monitor) performHealthCheck(ctx context.Context, backend *vmcp.Backend) {
	slog.Debug("performing health check for backend", "backend", backend.Name, "url", backend.BaseURL)

	// Check if circuit breaker allows health check
	// Status tracker handles circuit breaker logic based on its configuration
	if !m.statusTracker.ShouldAttemptHealthCheck(backend.ID, backend.Name) {
		return
	}

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
		slog.Debug("health check failed for backend", "backend", backend.Name, "error", err, "status", status)
		m.statusTracker.RecordFailure(backend.ID, backend.Name, status, err)
	} else {
		// Pass status to RecordSuccess - it may be healthy or degraded (from slow response)
		// RecordSuccess will further check for recovering state (had recent failures)
		slog.Debug("health check succeeded for backend", "backend", backend.Name, "status", status)
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

// QueryBackendStatus returns the current health status for a backend.
// Returns (status, exists) where exists indicates if the backend is being monitored.
// This method implements the StatusProvider interface for discovery middleware integration.
//
// Unlike GetBackendStatus, this method returns a boolean instead of an error,
// allowing callers to distinguish between "backend not monitored" (exists=false)
// and "backend is monitored but unhealthy" (exists=true, status=unhealthy).
func (m *Monitor) QueryBackendStatus(backendID string) (vmcp.BackendHealthStatus, bool) {
	return m.statusTracker.GetStatus(backendID)
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

// Routable returns the number of backends that can serve traffic.
// This includes healthy backends and unauthenticated backends (which are
// reachable but require per-request user auth, e.g., upstream OAuth).
func (s Summary) Routable() int {
	return s.Healthy + s.Unauthenticated
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
// Phase determination (unauthenticated backends are routable — they need per-request user auth
// but are reachable and running):
// - Ready: All backends healthy or unauthenticated, or no backends configured (cold start)
// - Pending: Backends configured but no health check data yet (waiting for first check)
// - Degraded: Some backends routable (healthy/unauthenticated), some degraded/unhealthy
// - Failed: No routable backends (and at least one backend exists)
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
	m.backendsMu.RLock()
	configuredBackendCount := len(m.backends)
	m.backendsMu.RUnlock()

	phase := determinePhase(summary, configuredBackendCount)
	message := formatStatusMessage(summary, phase, configuredBackendCount)
	discoveredBackends := m.convertToDiscoveredBackends(allStates)
	conditions := buildConditions(summary, phase, configuredBackendCount)

	return &vmcp.Status{
		Phase:              phase,
		Message:            message,
		Conditions:         conditions,
		DiscoveredBackends: discoveredBackends,
		BackendCount:       int32(summary.Routable()), //nolint:gosec // routable count is bounded by backend list size
		Timestamp:          time.Now(),
	}
}

// determinePhase determines the overall phase based on backend health.
// Unauthenticated backends are treated as routable — they are reachable and running,
// they just require per-request user auth (e.g., upstream OAuth).
// Takes both the health summary and the count of configured backends to distinguish:
// - No backends configured (configuredCount==0): Ready (cold start)
// - Backends configured but no health data (configuredCount>0 && summary.Total==0): Pending
// - Has health data: Ready/Degraded/Failed based on routable (healthy + unauthenticated) count
func determinePhase(summary Summary, configuredBackendCount int) vmcp.Phase {
	if summary.Total == 0 {
		// No health data yet - distinguish cold start from waiting for first check
		if configuredBackendCount == 0 {
			return vmcp.PhaseReady // True cold start - no backends configured
		}
		return vmcp.PhasePending // Backends configured but health checks not complete
	}

	if summary.Routable() == summary.Total {
		return vmcp.PhaseReady
	}
	if summary.Routable() == 0 {
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
		if summary.Unauthenticated == 0 {
			return fmt.Sprintf("All %d %s healthy", summary.Healthy, pluralBackend(summary.Healthy))
		}
		if summary.Healthy == 0 {
			return fmt.Sprintf("All %d %s require authentication",
				summary.Unauthenticated, pluralBackend(summary.Unauthenticated))
		}
		return fmt.Sprintf("%d %s healthy, %d require authentication",
			summary.Healthy, pluralBackend(summary.Healthy), summary.Unauthenticated)
	}

	// Format non-routable backend counts (shared by Failed and Degraded)
	nonRoutableDetails := fmt.Sprintf("%d degraded, %d unhealthy, %d unknown",
		summary.Degraded, summary.Unhealthy, summary.Unknown)

	if phase == vmcp.PhaseFailed {
		return fmt.Sprintf("No routable backends (%s)", nonRoutableDetails)
	}
	// Degraded
	return fmt.Sprintf("%d/%d backends routable (%s)", summary.Routable(), summary.Total, nonRoutableDetails)
}

// convertToDiscoveredBackends converts backend health states to DiscoveredBackend format.
// Iterates over all backends that have health state. Backends are removed from the status
// tracker when they're no longer being monitored (via UpdateBackends), so this only includes
// backends that are currently tracked or in the process of being removed.
func (m *Monitor) convertToDiscoveredBackends(allStates map[string]*State) []vmcp.DiscoveredBackend {
	discoveredBackends := make([]vmcp.DiscoveredBackend, 0, len(allStates))

	// Lock m.backends for reading to create a lookup map
	m.backendsMu.RLock()
	backendsByID := make(map[string]vmcp.Backend, len(m.backends))
	for _, b := range m.backends {
		backendsByID[b.ID] = b
	}
	m.backendsMu.RUnlock()

	// Iterate over all backends with health state
	for backendID, state := range allStates {
		// Try to get backend info from current backends
		backend, exists := backendsByID[backendID]
		if !exists {
			// Backend not in current list - this should be rare now that we update
			// m.backends before starting goroutines and ignore results for removed backends.
			// Keep as defensive fallback.
			discoveredBackends = append(discoveredBackends, vmcp.DiscoveredBackend{
				Name:                backendID,
				URL:                 "",
				Status:              state.Status.ToCRDStatus(),
				AuthConfigRef:       "",
				AuthType:            "",
				LastHealthCheck:     metav1.NewTime(state.LastCheckTime),
				Message:             formatBackendMessage(state),
				CircuitBreakerState: string(state.CircuitState),
				CircuitLastChanged:  metav1.NewTime(state.CircuitLastChanged),
				ConsecutiveFailures: state.ConsecutiveFailures,
			})
			continue
		}

		authConfigRef, authType := extractAuthInfo(backend)

		discoveredBackends = append(discoveredBackends, vmcp.DiscoveredBackend{
			Name:                backend.Name,
			URL:                 backend.BaseURL,
			Status:              state.Status.ToCRDStatus(),
			AuthConfigRef:       authConfigRef,
			AuthType:            authType,
			LastHealthCheck:     metav1.NewTime(state.LastCheckTime),
			Message:             formatBackendMessage(state),
			CircuitBreakerState: string(state.CircuitState),
			CircuitLastChanged:  metav1.NewTime(state.CircuitLastChanged),
			ConsecutiveFailures: state.ConsecutiveFailures,
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
// This returns generic error categories to avoid exposing sensitive error details in status.
// Detailed errors are logged when they occur (in performHealthCheck) for debugging.
// pluralBackend returns "backend" or "backends" based on count.
func pluralBackend(n int) string {
	if n == 1 {
		return "backend"
	}
	return "backends"
}

func formatBackendMessage(state *State) string {
	// Build base message
	var baseMsg string

	if state.LastError != nil {
		// Categorize error using errors.Is() for generic status messages
		// The detailed error is already logged in performHealthCheck for debugging
		category := categorizeErrorForMessage(state.LastError)
		if state.ConsecutiveFailures > 1 {
			baseMsg = fmt.Sprintf("%s (failures: %d)", category, state.ConsecutiveFailures)
		} else {
			baseMsg = category
		}
	} else {
		switch state.Status {
		case vmcp.BackendHealthy:
			baseMsg = "Healthy"
		case vmcp.BackendDegraded:
			if state.ConsecutiveFailures > 0 {
				baseMsg = fmt.Sprintf("Recovering from %d failures", state.ConsecutiveFailures)
			} else {
				baseMsg = "Degraded performance"
			}
		case vmcp.BackendUnhealthy:
			baseMsg = "Unhealthy"
		case vmcp.BackendUnauthenticated:
			baseMsg = "Authentication required"
		case vmcp.BackendUnknown:
			baseMsg = "Unknown"
		default:
			baseMsg = string(state.Status)
		}
	}

	// Prepend circuit breaker state if relevant
	switch state.CircuitState {
	case CircuitOpen:
		return fmt.Sprintf("Circuit breaker OPEN - %s", baseMsg)
	case CircuitHalfOpen:
		return fmt.Sprintf("Circuit breaker testing recovery - %s", baseMsg)
	case CircuitClosed, "":
		// Circuit closed or circuit breaker disabled - no prefix needed
		return baseMsg
	default:
		return baseMsg
	}
}

// categorizeErrorForMessage returns a generic error category message based on error type.
// This prevents exposing sensitive error details (like URLs, credentials, etc.) in status messages.
func categorizeErrorForMessage(err error) string {
	if err == nil {
		return "Unknown error"
	}

	// Authentication/Authorization errors
	if errors.Is(err, vmcp.ErrAuthenticationFailed) || errors.Is(err, vmcp.ErrAuthorizationFailed) {
		return "Authentication failed"
	}
	if vmcp.IsAuthenticationError(err) {
		return "Authentication failed"
	}

	// Timeout errors
	if errors.Is(err, vmcp.ErrTimeout) {
		return "Health check timed out"
	}
	if vmcp.IsTimeoutError(err) {
		return "Health check timed out"
	}

	// Cancellation errors
	if errors.Is(err, vmcp.ErrCancelled) {
		return "Health check cancelled"
	}

	// Connection/availability errors
	if errors.Is(err, vmcp.ErrBackendUnavailable) {
		return "Backend unavailable"
	}
	if vmcp.IsConnectionError(err) {
		return "Connection failed"
	}

	// Generic fallback
	return "Health check failed"
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
		// Distinguish cold start (no backends configured) from having routable backends
		if summary.Total == 0 && configuredBackendCount == 0 {
			readyCondition.Message = "Ready, no backends configured"
		} else if summary.Unauthenticated == 0 {
			readyCondition.Message = fmt.Sprintf("All %d %s are healthy",
				summary.Healthy, pluralBackend(summary.Healthy))
		} else if summary.Healthy == 0 {
			readyCondition.Message = fmt.Sprintf("All %d %s require authentication",
				summary.Unauthenticated, pluralBackend(summary.Unauthenticated))
		} else {
			readyCondition.Message = fmt.Sprintf("%d %s healthy, %d require authentication",
				summary.Healthy, pluralBackend(summary.Healthy), summary.Unauthenticated)
		}
	case vmcp.PhaseDegraded:
		readyCondition.Reason = "SomeBackendsUnhealthy"
		readyCondition.Message = fmt.Sprintf("%d/%d backends routable", summary.Routable(), summary.Total)
	case vmcp.PhaseFailed:
		readyCondition.Reason = "NoHealthyBackends"
		readyCondition.Message = "No routable backends available"
	case vmcp.PhasePending:
		readyCondition.Reason = "BackendsPending"
		readyCondition.Message = fmt.Sprintf("Waiting for initial health checks (%d backends configured)", configuredBackendCount)
	default:
		// Unknown phase - use default values set above
		readyCondition.Reason = "BackendsUnhealthy"
		readyCondition.Message = "Backend status unknown"
	}

	conditions = append(conditions, readyCondition)

	// BackendsDiscovered condition - indicates whether backend discovery completed
	// This is always true once the health monitor is running, as backends are discovered
	// during aggregator initialization before the monitor starts.
	backendsDiscoveredCondition := metav1.Condition{
		Type:               vmcp.ConditionTypeBackendsDiscovered,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: now,
		Reason:             "BackendsDiscovered",
		Message:            fmt.Sprintf("Discovered %d backends", configuredBackendCount),
	}
	if configuredBackendCount == 0 {
		// No backends configured (cold start is valid)
		backendsDiscoveredCondition.Message = "No backends configured"
	}
	conditions = append(conditions, backendsDiscoveredCondition)

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

// backendChanged returns true if the backend's health-check-relevant properties have changed.
// This is used by UpdateBackends to detect when an existing backend needs its monitoring
// goroutine restarted (e.g., URL updated after operator reconcile).
func backendChanged(old, updated vmcp.Backend) bool {
	return old.BaseURL != updated.BaseURL || old.TransportType != updated.TransportType
}

package health

import (
	"context"
	"fmt"
	"sync"
	"time"

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
func IsHealthCheck(ctx context.Context) bool {
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

	// Create health checker with degraded threshold
	checker := NewHealthChecker(client, config.Timeout, config.DegradedThreshold)

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

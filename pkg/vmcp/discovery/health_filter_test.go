package discovery

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

// mockHealthProvider is a simple test implementation of HealthStatusProvider
type mockHealthProvider struct {
	statuses map[string]vmcp.BackendHealthStatus
	errors   map[string]error
}

func newMockHealthProvider() *mockHealthProvider {
	return &mockHealthProvider{
		statuses: make(map[string]vmcp.BackendHealthStatus),
		errors:   make(map[string]error),
	}
}

func (m *mockHealthProvider) GetBackendStatus(backendID string) (vmcp.BackendHealthStatus, error) {
	if err, ok := m.errors[backendID]; ok {
		return vmcp.BackendUnknown, err
	}
	if status, ok := m.statuses[backendID]; ok {
		return status, nil
	}
	return vmcp.BackendUnknown, errors.New("backend not found")
}

func (m *mockHealthProvider) IsBackendHealthy(backendID string) bool {
	status, err := m.GetBackendStatus(backendID)
	if err != nil {
		return false
	}
	return status == vmcp.BackendHealthy || status == vmcp.BackendDegraded
}

func (m *mockHealthProvider) setStatus(backendID string, status vmcp.BackendHealthStatus) {
	m.statuses[backendID] = status
	delete(m.errors, backendID)
}

func (m *mockHealthProvider) setError(backendID string, err error) {
	m.errors[backendID] = err
	delete(m.statuses, backendID)
}

func TestFilterHealthyBackends_NoHealthMonitoring(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
		{ID: "backend3", Name: "Backend 3"},
	}

	// When health monitoring is disabled (nil provider), all backends should be returned
	filtered := FilterHealthyBackends(backends, nil)

	assert.Len(t, filtered, 3, "all backends should be included when health monitoring is disabled")
	assert.Equal(t, backends, filtered, "backends should be unchanged")
}

func TestFilterHealthyBackends_AllHealthy(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
		{ID: "backend3", Name: "Backend 3"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setStatus("backend2", vmcp.BackendHealthy)
	healthProvider.setStatus("backend3", vmcp.BackendHealthy)

	filtered := FilterHealthyBackends(backends, healthProvider)

	assert.Len(t, filtered, 3, "all healthy backends should be included")
	assert.Equal(t, backends, filtered, "all backends should be present")
}

func TestFilterHealthyBackends_MixedHealthStatus(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
		{ID: "backend3", Name: "Backend 3"},
		{ID: "backend4", Name: "Backend 4"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setStatus("backend2", vmcp.BackendUnhealthy)
	healthProvider.setStatus("backend3", vmcp.BackendDegraded)
	healthProvider.setStatus("backend4", vmcp.BackendUnauthenticated)

	filtered := FilterHealthyBackends(backends, healthProvider)

	// Should include: healthy (backend1) and degraded (backend3)
	// Should exclude: unhealthy (backend2) and unauthenticated (backend4)
	require.Len(t, filtered, 2, "only healthy and degraded backends should be included")
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "backend3", filtered[1].ID)
}

func TestFilterHealthyBackends_ExcludesUnhealthy(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
		{ID: "backend3", Name: "Backend 3"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setStatus("backend2", vmcp.BackendUnhealthy)
	healthProvider.setStatus("backend3", vmcp.BackendHealthy)

	filtered := FilterHealthyBackends(backends, healthProvider)

	require.Len(t, filtered, 2, "unhealthy backend should be excluded")
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "backend3", filtered[1].ID)
}

func TestFilterHealthyBackends_ExcludesUnauthenticated(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setStatus("backend2", vmcp.BackendUnauthenticated)

	filtered := FilterHealthyBackends(backends, healthProvider)

	require.Len(t, filtered, 1, "unauthenticated backend should be excluded")
	assert.Equal(t, "backend1", filtered[0].ID)
}

func TestFilterHealthyBackends_IncludesDegraded(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setStatus("backend2", vmcp.BackendDegraded)

	filtered := FilterHealthyBackends(backends, healthProvider)

	// Degraded backends are still functional (just slow), so they should be included
	require.Len(t, filtered, 2, "degraded backends should be included")
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "backend2", filtered[1].ID)
}

func TestFilterHealthyBackends_IncludesUnknown(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setStatus("backend2", vmcp.BackendUnknown)

	filtered := FilterHealthyBackends(backends, healthProvider)

	// Unknown backends should be included (health not yet determined, give them a chance)
	require.Len(t, filtered, 2, "unknown backends should be included")
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "backend2", filtered[1].ID)
}

func TestFilterHealthyBackends_BackendNotFoundInMonitor(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	// backend2 not in health monitor (GetBackendStatus will return error)

	filtered := FilterHealthyBackends(backends, healthProvider)

	// Backend not found in monitor should be included (new backend during transitions)
	require.Len(t, filtered, 2, "backends not found in monitor should be included")
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "backend2", filtered[1].ID)
}

func TestFilterHealthyBackends_AllUnhealthy(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendUnhealthy)
	healthProvider.setStatus("backend2", vmcp.BackendUnhealthy)

	filtered := FilterHealthyBackends(backends, healthProvider)

	assert.Len(t, filtered, 0, "all unhealthy backends should be excluded")
}

func TestFilterHealthyBackends_EmptyBackendList(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{}
	healthProvider := newMockHealthProvider()

	filtered := FilterHealthyBackends(backends, healthProvider)

	assert.Len(t, filtered, 0, "empty input should return empty output")
	assert.NotNil(t, filtered, "result should not be nil")
}

func TestFilterHealthyBackends_PreservesBackendData(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{
			ID:            "backend1",
			Name:          "Backend 1",
			BaseURL:       "http://backend1:8080",
			TransportType: "sse",
			HealthStatus:  vmcp.BackendHealthy,
			Metadata:      map[string]string{"env": "prod"},
		},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)

	filtered := FilterHealthyBackends(backends, healthProvider)

	require.Len(t, filtered, 1)
	// Verify all backend data is preserved
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "Backend 1", filtered[0].Name)
	assert.Equal(t, "http://backend1:8080", filtered[0].BaseURL)
	assert.Equal(t, "sse", filtered[0].TransportType)
	assert.Equal(t, vmcp.BackendHealthy, filtered[0].HealthStatus)
	assert.Equal(t, map[string]string{"env": "prod"}, filtered[0].Metadata)
}

func TestFilterHealthyBackends_ErrorRetrievingStatus(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1"},
		{ID: "backend2", Name: "Backend 2"},
	}

	healthProvider := newMockHealthProvider()
	healthProvider.setStatus("backend1", vmcp.BackendHealthy)
	healthProvider.setError("backend2", errors.New("health monitor error"))

	filtered := FilterHealthyBackends(backends, healthProvider)

	// Backend with error should be included (assume healthy during error conditions)
	require.Len(t, filtered, 2, "backends with health monitor errors should be included")
	assert.Equal(t, "backend1", filtered[0].ID)
	assert.Equal(t, "backend2", filtered[1].ID)
}

func TestFilterHealthyBackends_NilTypedPointer(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		{ID: "backend1", Name: "Backend 1", HealthStatus: vmcp.BackendHealthy},
	}

	// Create a nil typed pointer (*health.Monitor)(nil)
	// This is wrapped in an interface and is NOT caught by simple nil checks
	// This simulates the common bug where a nil pointer is passed to an interface parameter
	var nilMonitor *mockHealthProvider
	var provider health.StatusProvider = nilMonitor

	// Should not panic and should return all backends (health monitoring disabled)
	filtered := FilterHealthyBackends(backends, provider)

	assert.Len(t, filtered, 1, "Should return all backends when provider is nil pointer")
	assert.Equal(t, "backend1", filtered[0].ID)
}

func TestIsProviderInitialized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider health.StatusProvider
		expected bool
	}{
		{
			name:     "Nil interface",
			provider: nil,
			expected: false,
		},
		{
			name:     "Nil typed pointer",
			provider: (*mockHealthProvider)(nil),
			expected: false,
		},
		{
			name: "Initialized provider",
			provider: func() health.StatusProvider {
				return newMockHealthProvider()
			}(),
			expected: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := health.IsProviderInitialized(tt.provider)
			assert.Equal(t, tt.expected, result)
		})
	}
}

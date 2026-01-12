package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewK8sReporter_Validation(t *testing.T) {
	t.Parallel()

	// Note: We can't easily test NewK8sReporter with a real rest.Config in unit tests
	// because it requires a Kubernetes cluster. The function is tested in e2e tests.
	// This test documents the validation logic.

	tests := []struct {
		name      string
		expectErr bool
	}{
		{
			name:      "validates input parameters",
			expectErr: true, // Would fail with nil restConfig
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Test validation by passing nil config
			_, err := NewK8sReporter(nil, "test", "default")
			if tt.expectErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "restConfig cannot be nil")
			}
		})
	}
}

func TestNewK8sReporter_EmptyParameters(t *testing.T) {
	t.Parallel()

	// Create a minimal rest.Config for parameter validation tests
	// We need a non-nil config to get past the restConfig validation
	// and test the parameter validation logic
	mockConfig := &rest.Config{}

	tests := []struct {
		name      string
		vmcpName  string
		namespace string
		expectErr string
	}{
		{
			name:      "empty name",
			vmcpName:  "",
			namespace: "default",
			expectErr: "name cannot be empty",
		},
		{
			name:      "empty namespace",
			vmcpName:  "test",
			namespace: "",
			expectErr: "namespace cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewK8sReporter(mockConfig, tt.vmcpName, tt.namespace)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestK8sReporter_UpdateStatus(t *testing.T) {
	t.Parallel()

	// Create fake client with scheme
	scheme := runtime.NewScheme()
	err := mcpv1alpha1.AddToScheme(scheme)
	require.NoError(t, err)

	// Create a VirtualMCPServer resource
	vmcpResource := &mcpv1alpha1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-vmcp",
			Namespace:  "default",
			Generation: 5,
		},
		Spec: mcpv1alpha1.VirtualMCPServerSpec{
			IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
				Type: "anonymous",
			},
		},
		Status: mcpv1alpha1.VirtualMCPServerStatus{
			Phase: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcpResource).
		WithStatusSubresource(vmcpResource).
		Build()

	reporter := &K8sReporter{
		client:    fakeClient,
		name:      "test-vmcp",
		namespace: "default",
		stopChan:  make(chan struct{}),
	}

	// Create runtime status
	runtimeStatus := &RuntimeStatus{
		Phase:   PhaseReady,
		Message: "All systems operational",
		Conditions: []Condition{
			{
				Type:               ConditionServerReady,
				Status:             ConditionTrue,
				LastTransitionTime: time.Now(),
				Reason:             "ServerStarted",
				Message:            "Server is ready",
			},
		},
		DiscoveredBackends: []DiscoveredBackend{
			{
				ID:            "backend-1",
				Name:          "Test Backend",
				HealthStatus:  vmcptypes.BackendHealthy,
				BaseURL:       "http://backend1:8080",
				TransportType: "http",
				ToolCount:     5,
				LastCheckTime: time.Now(),
			},
		},
		HealthyBackendCount:   1,
		UnhealthyBackendCount: 0,
		LastUpdateTime:        time.Now(),
	}

	// Report status
	ctx := context.Background()
	err = reporter.Report(ctx, runtimeStatus)
	require.NoError(t, err)

	// Verify status was updated
	updated := &mcpv1alpha1.VirtualMCPServer{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-vmcp", Namespace: "default"}, updated)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.VirtualMCPServerPhaseReady, updated.Status.Phase)
	assert.Equal(t, "All systems operational", updated.Status.Message)
	assert.Equal(t, 1, updated.Status.BackendCount)
	assert.Len(t, updated.Status.DiscoveredBackends, 1)
	assert.Equal(t, "Test Backend", updated.Status.DiscoveredBackends[0].Name)
	assert.Equal(t, mcpv1alpha1.BackendStatusReady, updated.Status.DiscoveredBackends[0].Status)
	assert.Len(t, updated.Status.Conditions, 1)
	assert.Equal(t, int64(5), updated.Status.ObservedGeneration)
}

func TestK8sReporter_Report_NilStatus(t *testing.T) {
	t.Parallel()

	reporter := &K8sReporter{
		name:      "test",
		namespace: "default",
	}

	err := reporter.Report(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtimeStatus cannot be nil")
}

func TestConvertPhase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Phase
		expected mcpv1alpha1.VirtualMCPServerPhase
	}{
		{
			name:     "PhaseReady",
			input:    PhaseReady,
			expected: mcpv1alpha1.VirtualMCPServerPhaseReady,
		},
		{
			name:     "PhaseDegraded",
			input:    PhaseDegraded,
			expected: mcpv1alpha1.VirtualMCPServerPhaseDegraded,
		},
		{
			name:     "PhaseFailed",
			input:    PhaseFailed,
			expected: mcpv1alpha1.VirtualMCPServerPhaseFailed,
		},
		{
			name:     "PhaseUnknown",
			input:    PhaseUnknown,
			expected: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
		{
			name:     "PhaseStarting",
			input:    PhaseStarting,
			expected: mcpv1alpha1.VirtualMCPServerPhasePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertPhase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertBackendHealthStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    vmcptypes.BackendHealthStatus
		expected string
	}{
		{
			name:     "BackendHealthy",
			input:    vmcptypes.BackendHealthy,
			expected: mcpv1alpha1.BackendStatusReady,
		},
		{
			name:     "BackendDegraded",
			input:    vmcptypes.BackendDegraded,
			expected: mcpv1alpha1.BackendStatusDegraded,
		},
		{
			name:     "BackendUnhealthy",
			input:    vmcptypes.BackendUnhealthy,
			expected: mcpv1alpha1.BackendStatusUnavailable,
		},
		{
			name:     "BackendUnauthenticated",
			input:    vmcptypes.BackendUnauthenticated,
			expected: mcpv1alpha1.BackendStatusUnavailable,
		},
		{
			name:     "BackendUnknown",
			input:    vmcptypes.BackendUnknown,
			expected: mcpv1alpha1.BackendStatusUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertBackendHealthStatus(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertConditionStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    ConditionStatus
		expected metav1.ConditionStatus
	}{
		{
			name:     "ConditionTrue",
			input:    ConditionTrue,
			expected: metav1.ConditionTrue,
		},
		{
			name:     "ConditionFalse",
			input:    ConditionFalse,
			expected: metav1.ConditionFalse,
		},
		{
			name:     "ConditionUnknown",
			input:    ConditionUnknown,
			expected: metav1.ConditionUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := convertConditionStatus(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestK8sReporter_Start_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		interval   time.Duration
		statusFunc func() *RuntimeStatus
		expectErr  string
	}{
		{
			name:       "nil statusFunc",
			interval:   time.Second,
			statusFunc: nil,
			expectErr:  "statusFunc cannot be nil",
		},
		{
			name:     "zero interval",
			interval: 0,
			statusFunc: func() *RuntimeStatus {
				return &RuntimeStatus{Phase: PhaseReady}
			},
			expectErr: "interval must be positive",
		},
		{
			name:     "negative interval",
			interval: -1 * time.Second,
			statusFunc: func() *RuntimeStatus {
				return &RuntimeStatus{Phase: PhaseReady}
			},
			expectErr: "interval must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create new reporter for each test
			r := &K8sReporter{
				name:      "test",
				namespace: "default",
				stopChan:  make(chan struct{}),
			}

			err := r.Start(context.Background(), tt.interval, tt.statusFunc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectErr)
		})
	}
}

func TestK8sReporter_ImplementsInterface(t *testing.T) {
	t.Parallel()

	// Verify K8sReporter implements Reporter interface
	var _ Reporter = (*K8sReporter)(nil)
}

func TestK8sReporter_Stop_Multiple(t *testing.T) {
	t.Parallel()

	// Create a reporter with minimal setup
	reporter := &K8sReporter{
		name:      "test",
		namespace: "default",
		stopChan:  make(chan struct{}),
	}

	// Calling Stop multiple times should not panic
	reporter.Stop()
	reporter.Stop()
	reporter.Stop()

	// If we reach here without panic, the test passes
}

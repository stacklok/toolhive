package status

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewK8SReporter(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	tests := []struct {
		name    string
		cfg     K8SReporterConfig
		wantErr bool
	}{
		{
			name: "valid configuration",
			cfg: K8SReporterConfig{
				Client:    fakeClient,
				Name:      "test-vmcp",
				Namespace: "default",
			},
			wantErr: false,
		},
		{
			name: "valid configuration with periodic reporting",
			cfg: K8SReporterConfig{
				Client:           fakeClient,
				Name:             "test-vmcp",
				Namespace:        "default",
				PeriodicInterval: 30 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "missing client",
			cfg: K8SReporterConfig{
				Name:      "test-vmcp",
				Namespace: "default",
			},
			wantErr: true,
		},
		{
			name: "missing name",
			cfg: K8SReporterConfig{
				Client:    fakeClient,
				Namespace: "default",
			},
			wantErr: true,
		},
		{
			name: "missing namespace",
			cfg: K8SReporterConfig{
				Client: fakeClient,
				Name:   "test-vmcp",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reporter, err := NewK8SReporter(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewK8SReporter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && reporter == nil {
				t.Error("NewK8SReporter() returned nil reporter without error")
			}
		})
	}
}

func TestK8SReporter_ReportStatus(t *testing.T) {
	t.Parallel()

	tests := []struct{
		name    string
		status  *Status
		wantErr bool
	}{
		{
			name: "report ready status with backends",
			status: &Status{
				Phase:   PhaseReady,
				Message: "Server is ready",
				DiscoveredBackends: []DiscoveredBackend{
					{
						Name:            "backend1",
						URL:             "http://backend1:8080",
						Status:          BackendStatusReady,
						AuthType:        "oauth2",
						AuthConfigRef:   "auth-config-1",
						LastHealthCheck: time.Now(),
					},
					{
						Name:            "backend2",
						URL:             "http://backend2:8080",
						Status:          BackendStatusReady,
						AuthType:        "unauthenticated",
						LastHealthCheck: time.Now(),
					},
				},
				Conditions: []Condition{
					{
						Type:               ConditionTypeBackendsDiscovered,
						Status:             metav1.ConditionTrue,
						Reason:             ReasonBackendDiscoverySucceeded,
						Message:            "Discovered 2 backends",
						LastTransitionTime: time.Now(),
					},
					{
						Type:               ConditionTypeReady,
						Status:             metav1.ConditionTrue,
						Reason:             ReasonServerReady,
						Message:            "Server is ready",
						LastTransitionTime: time.Now(),
					},
				},
				ObservedGeneration: 1,
				Timestamp:          time.Now(),
			},
			wantErr: false,
		},
		{
			name: "report degraded status with some backends unavailable",
			status: &Status{
				Phase:   PhaseDegraded,
				Message: "Some backends unavailable",
				DiscoveredBackends: []DiscoveredBackend{
					{
						Name:            "backend1",
						URL:             "http://backend1:8080",
						Status:          BackendStatusReady,
						LastHealthCheck: time.Now(),
					},
					{
						Name:            "backend2",
						URL:             "http://backend2:8080",
						Status:          BackendStatusUnavailable,
						Message:         "Connection refused",
						LastHealthCheck: time.Now(),
					},
				},
				Conditions: []Condition{
					{
						Type:               ConditionTypeReady,
						Status:             metav1.ConditionFalse,
						Reason:             ReasonServerDegraded,
						Message:            "1 of 2 backends unavailable",
						LastTransitionTime: time.Now(),
					},
				},
				ObservedGeneration: 1,
				Timestamp:          time.Now(),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			// Create scheme and fake client for this subtest
			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)

			// Create a VirtualMCPServer resource to update
			vmcp := &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-vmcp",
					Namespace:  "default",
					Generation: 1,
				},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					GroupRef: mcpv1alpha1.GroupRef{
						Name: "test-group",
					},
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type: "anonymous",
					},
				},
				Status: mcpv1alpha1.VirtualMCPServerStatus{
					Phase:   mcpv1alpha1.VirtualMCPServerPhasePending,
					Message: "Initializing",
				},
			}

			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(vmcp).WithStatusSubresource(vmcp).Build()

			reporter, err := NewK8SReporter(K8SReporterConfig{
				Client:    fakeClient,
				Name:      "test-vmcp",
				Namespace: "default",
			})
			if err != nil {
				t.Fatalf("Failed to create K8SReporter: %v", err)
			}

			err = reporter.ReportStatus(ctx, tt.status)
			if (err != nil) != tt.wantErr {
				t.Errorf("K8SReporter.ReportStatus() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				// Verify status was updated
				updated := &mcpv1alpha1.VirtualMCPServer{}
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      "test-vmcp",
					Namespace: "default",
				}, updated)
				if err != nil {
					t.Fatalf("Failed to get updated VirtualMCPServer: %v", err)
				}

				if string(updated.Status.Phase) != string(tt.status.Phase) {
					t.Errorf("Phase not updated: got %v, want %v", updated.Status.Phase, tt.status.Phase)
				}

				if updated.Status.Message != tt.status.Message {
					t.Errorf("Message not updated: got %v, want %v", updated.Status.Message, tt.status.Message)
				}

				if len(updated.Status.DiscoveredBackends) != len(tt.status.DiscoveredBackends) {
					t.Errorf("DiscoveredBackends count mismatch: got %v, want %v",
						len(updated.Status.DiscoveredBackends), len(tt.status.DiscoveredBackends))
				}

				if updated.Status.BackendCount != len(tt.status.DiscoveredBackends) {
					t.Errorf("BackendCount not updated: got %v, want %v",
						updated.Status.BackendCount, len(tt.status.DiscoveredBackends))
				}

				if updated.Status.ObservedGeneration != tt.status.ObservedGeneration {
					t.Errorf("ObservedGeneration not updated: got %v, want %v",
						updated.Status.ObservedGeneration, tt.status.ObservedGeneration)
				}
			}
		})
	}
}

func TestK8SReporter_ReportStatus_ResourceNotFound(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = mcpv1alpha1.AddToScheme(scheme)

	// Create client without the resource
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	reporter, err := NewK8SReporter(K8SReporterConfig{
		Client:    fakeClient,
		Name:      "nonexistent-vmcp",
		Namespace: "default",
	})
	if err != nil {
		t.Fatalf("Failed to create K8SReporter: %v", err)
	}

	status := &Status{
		Phase:     PhaseReady,
		Message:   "Server is ready",
		Timestamp: time.Now(),
	}

	ctx := context.Background()
	err = reporter.ReportStatus(ctx, status)
	if err == nil {
		t.Error("K8SReporter.ReportStatus() expected error for nonexistent resource, got nil")
	}
}

func TestK8SReporter_StartStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		periodicInterval time.Duration
	}{
		{
			name:             "without periodic reporting",
			periodicInterval: 0,
		},
		{
			name:             "with periodic reporting",
			periodicInterval: 100 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			reporter, err := NewK8SReporter(K8SReporterConfig{
				Client:           fakeClient,
				Name:             "test-vmcp",
				Namespace:        "default",
				PeriodicInterval: tt.periodicInterval,
			})
			if err != nil {
				t.Fatalf("Failed to create K8SReporter: %v", err)
			}

			ctx := context.Background()

			// Start should not return error
			err = reporter.Start(ctx)
			if err != nil {
				t.Errorf("K8SReporter.Start() unexpected error = %v", err)
			}

			// Stop should not return error
			err = reporter.Stop(ctx)
			if err != nil {
				t.Errorf("K8SReporter.Stop() unexpected error = %v", err)
			}
		})
	}
}

func TestK8SReporter_ImplementsInterface(t *testing.T) {
	t.Parallel()
	var _ Reporter = (*K8SReporter)(nil)
}

func TestK8SReporter_MapConditions(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name               string
		newConditions      []Condition
		existingConditions []metav1.Condition
		wantLen            int
		wantTypes          []string
	}{
		{
			name: "add new condition to empty list",
			newConditions: []Condition{
				{
					Type:               ConditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             ReasonServerReady,
					Message:            "Server is ready",
					LastTransitionTime: now,
				},
			},
			existingConditions: []metav1.Condition{},
			wantLen:            1,
			wantTypes:          []string{ConditionTypeReady},
		},
		{
			name: "update existing condition",
			newConditions: []Condition{
				{
					Type:               ConditionTypeReady,
					Status:             metav1.ConditionFalse,
					Reason:             ReasonServerDegraded,
					Message:            "Server degraded",
					LastTransitionTime: now,
				},
			},
			existingConditions: []metav1.Condition{
				{
					Type:               ConditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             ReasonServerReady,
					Message:            "Server is ready",
					LastTransitionTime: metav1.Time{Time: now.Add(-1 * time.Minute)},
				},
			},
			wantLen:   1,
			wantTypes: []string{ConditionTypeReady},
		},
		{
			name: "preserve existing condition not in new list",
			newConditions: []Condition{
				{
					Type:               ConditionTypeReady,
					Status:             metav1.ConditionTrue,
					Reason:             ReasonServerReady,
					Message:            "Server is ready",
					LastTransitionTime: now,
				},
			},
			existingConditions: []metav1.Condition{
				{
					Type:               ConditionTypeBackendsDiscovered,
					Status:             metav1.ConditionTrue,
					Reason:             ReasonBackendDiscoverySucceeded,
					Message:            "Backends discovered",
					LastTransitionTime: metav1.Time{Time: now.Add(-1 * time.Minute)},
				},
			},
			wantLen:   2,
			wantTypes: []string{ConditionTypeBackendsDiscovered, ConditionTypeReady},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			_ = mcpv1alpha1.AddToScheme(scheme)
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			reporter, err := NewK8SReporter(K8SReporterConfig{
				Client:    fakeClient,
				Name:      "test-vmcp",
				Namespace: "default",
			})
			if err != nil {
				t.Fatalf("Failed to create K8SReporter: %v", err)
			}

			result := reporter.mapConditions(tt.newConditions, tt.existingConditions)

			if len(result) != tt.wantLen {
				t.Errorf("mapConditions() returned %v conditions, want %v", len(result), tt.wantLen)
			}

			// Verify all expected types are present
			resultTypes := make(map[string]bool)
			for _, cond := range result {
				resultTypes[cond.Type] = true
			}

			for _, wantType := range tt.wantTypes {
				if !resultTypes[wantType] {
					t.Errorf("mapConditions() missing expected condition type %v", wantType)
				}
			}
		})
	}
}

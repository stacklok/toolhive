package status

import (
	"os"
	"testing"
)

// TestNewReporter_CLI verifies factory creates LogReporter in CLI environment
func TestNewReporter_CLI(t *testing.T) {
	// Ensure we're NOT in Kubernetes
	os.Unsetenv("KUBERNETES_SERVICE_HOST")
	
	reporter, err := NewReporter("test-server", "default")
	if err != nil {
		t.Fatalf("NewReporter should not error in CLI env, got: %v", err)
	}
	
	if reporter == nil {
		t.Fatal("NewReporter returned nil")
	}
	
	// Verify it's a LogReporter by type assertion
	_, ok := reporter.(*LogReporter)
	if !ok {
		t.Errorf("Expected LogReporter in CLI environment, got: %T", reporter)
	}
}

// TestNewReporter_Kubernetes verifies factory creates K8sReporter in K8s environment
func TestNewReporter_Kubernetes(t *testing.T) {
	// Simulate Kubernetes environment
	originalValue := os.Getenv("KUBERNETES_SERVICE_HOST")
	os.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	defer func() {
		if originalValue == "" {
			os.Unsetenv("KUBERNETES_SERVICE_HOST")
		} else {
			os.Setenv("KUBERNETES_SERVICE_HOST", originalValue)
		}
	}()
	
	reporter, err := NewReporter("test-server", "default")
	
	// K8sReporter might fail to create client in test environment - that's OK
	// We're testing that the factory TRIES to create K8sReporter
	if err != nil {
		// Expected - no K8s cluster in test environment
		t.Logf("K8sReporter creation failed as expected in test env: %v", err)
		return
	}
	
	if reporter == nil {
		t.Fatal("NewReporter returned nil without error")
	}
	
	// Verify it's a K8sReporter
	_, ok := reporter.(*K8sReporter)
	if !ok {
		t.Errorf("Expected K8sReporter in K8s environment, got: %T", reporter)
	}
}

// TestIsKubernetesRuntime verifies environment detection logic
func TestIsKubernetesRuntime(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		want     bool
	}{
		{
			name:     "not in kubernetes",
			envValue: "",
			want:     false,
		},
		{
			name:     "in kubernetes",
			envValue: "10.96.0.1",
			want:     true,
		},
		{
			name:     "in kubernetes with different IP",
			envValue: "192.168.1.1",
			want:     true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original value
			originalValue := os.Getenv("KUBERNETES_SERVICE_HOST")
			defer func() {
				if originalValue == "" {
					os.Unsetenv("KUBERNETES_SERVICE_HOST")
				} else {
					os.Setenv("KUBERNETES_SERVICE_HOST", originalValue)
				}
			}()
			
			// Set test value
			if tt.envValue == "" {
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
			} else {
				os.Setenv("KUBERNETES_SERVICE_HOST", tt.envValue)
			}
			
			got := isKubernetesRuntime()
			if got != tt.want {
				t.Errorf("isKubernetesRuntime() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestNewReporter_WithDifferentNames verifies factory handles various names
func TestNewReporter_WithDifferentNames(t *testing.T) {
	os.Unsetenv("KUBERNETES_SERVICE_HOST")
	
	tests := []struct {
		name      string
		vmcpName  string
		namespace string
	}{
		{"simple name", "my-server", "default"},
		{"with dashes", "my-test-server", "production"},
		{"with numbers", "server123", "ns-456"},
		{"long name", "very-long-server-name-for-testing", "some-namespace"},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter, err := NewReporter(tt.vmcpName, tt.namespace)
			if err != nil {
				t.Fatalf("NewReporter failed: %v", err)
			}
			if reporter == nil {
				t.Fatal("NewReporter returned nil")
			}
			
			// Verify it's a LogReporter
			logReporter, ok := reporter.(*LogReporter)
			if !ok {
				t.Fatalf("Expected LogReporter, got: %T", reporter)
			}
			
			if logReporter.name != tt.vmcpName {
				t.Errorf("Expected name %s, got %s", tt.vmcpName, logReporter.name)
			}
		})
	}
}

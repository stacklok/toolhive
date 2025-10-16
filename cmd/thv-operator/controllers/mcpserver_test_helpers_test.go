package controllers

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// mockPlatformDetector is a mock implementation of PlatformDetector for testing
type mockPlatformDetector struct {
	platform kubernetes.Platform
	err      error
}

func (m *mockPlatformDetector) DetectPlatform(_ *rest.Config) (kubernetes.Platform, error) {
	return m.platform, m.err
}

// newTestMCPServerReconciler creates a properly initialized MCPServerReconciler for testing.
// This ensures all required fields are set, including the PlatformDetector.
//
//nolint:unparam // platform parameter is intentionally flexible for future test cases
func newTestMCPServerReconciler(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	platform kubernetes.Platform,
) *MCPServerReconciler {
	mockDetector := &mockPlatformDetector{
		platform: platform,
		err:      nil,
	}
	return &MCPServerReconciler{
		Client:           k8sClient,
		Scheme:           scheme,
		PlatformDetector: NewSharedPlatformDetectorWithDetector(mockDetector),
		ImageValidation:  validation.ImageValidationAlwaysAllow,
	}
}

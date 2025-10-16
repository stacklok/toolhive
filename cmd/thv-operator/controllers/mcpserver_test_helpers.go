package controllers

import (
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// newTestMCPServerReconciler creates a properly initialized MCPServerReconciler for testing.
// This ensures all required fields are set, including the PlatformDetector.
func newTestMCPServerReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	platform kubernetes.Platform,
) *MCPServerReconciler {
	mockDetector := &mockPlatformDetector{
		platform: platform,
		err:      nil,
	}
	return &MCPServerReconciler{
		Client:           client,
		Scheme:           scheme,
		PlatformDetector: NewSharedPlatformDetectorWithDetector(mockDetector),
		ImageValidation:  validation.ImageValidationAlwaysAllow,
	}
}

// newTestMCPServerReconcilerWithDetector creates a MCPServerReconciler with a custom platform detector for testing.
func newTestMCPServerReconcilerWithDetector(
	client client.Client,
	scheme *runtime.Scheme,
	detector kubernetes.PlatformDetector,
) *MCPServerReconciler {
	return &MCPServerReconciler{
		Client:           client,
		Scheme:           scheme,
		PlatformDetector: NewSharedPlatformDetectorWithDetector(detector),
		ImageValidation:  validation.ImageValidationAlwaysAllow,
	}
}

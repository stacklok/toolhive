package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestVirtualMCPServerPodTemplateSpecBuilder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		rawTemplate *runtime.RawExtension
		expectError bool
		expectNil   bool
	}{
		{
			name:        "nil template",
			rawTemplate: nil,
			expectError: false,
			expectNil:   true,
		},
		{
			name: "empty template",
			rawTemplate: &runtime.RawExtension{
				Raw: []byte(`{}`),
			},
			expectError: false,
			expectNil:   false, // Empty template is still returned since user provided it
		},
		{
			name: "template with node selector",
			rawTemplate: &runtime.RawExtension{
				Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
			},
			expectError: false,
			expectNil:   false,
		},
		{
			name: "invalid JSON",
			rawTemplate: &runtime.RawExtension{
				Raw: []byte(`{invalid json`),
			},
			expectError: true,
			expectNil:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			builder, err := NewVirtualMCPServerPodTemplateSpecBuilder(tt.rawTemplate)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			if err != nil {
				return
			}

			result := builder.Build()

			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestVirtualMCPServerPodTemplateSpecValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		podTemplateSpec  *runtime.RawExtension
		expectValidation bool
	}{
		{
			name:             "no PodTemplateSpec provided",
			podTemplateSpec:  nil,
			expectValidation: true,
		},
		{
			name: "valid PodTemplateSpec",
			podTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
			},
			expectValidation: true,
		},
		{
			name: "invalid PodTemplateSpec",
			podTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{invalid json`),
			},
			expectValidation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Test using the builder directly to avoid needing a full reconciler setup
			_, err := NewVirtualMCPServerPodTemplateSpecBuilder(tt.podTemplateSpec)

			if tt.expectValidation {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestVirtualMCPServerApplyPodTemplateSpec is covered by integration tests
// since it requires a full reconciler setup with scheme and client

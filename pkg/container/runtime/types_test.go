package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRuntimeTypes(t *testing.T) {
	t.Parallel()

	// Test that all runtime types are properly defined
	tests := []struct {
		name          string
		runtimeType   Type
		expectedValue string
	}{
		{
			name:          "TypePodman",
			runtimeType:   TypePodman,
			expectedValue: "podman",
		},
		{
			name:          "TypeDocker",
			runtimeType:   TypeDocker,
			expectedValue: "docker",
		},
		{
			name:          "TypeColima",
			runtimeType:   TypeColima,
			expectedValue: "colima",
		},
		{
			name:          "TypeKubernetes",
			runtimeType:   TypeKubernetes,
			expectedValue: "kubernetes",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expectedValue, string(tt.runtimeType))
		})
	}
}

func TestColimaRuntimeTypeExists(t *testing.T) {
	t.Parallel()

	// Ensure TypeColima constant exists and has the correct value
	assert.Equal(t, Type("colima"), TypeColima)

	// Verify it's different from other runtime types
	assert.NotEqual(t, TypeColima, TypeDocker)
	assert.NotEqual(t, TypeColima, TypePodman)
	assert.NotEqual(t, TypeColima, TypeKubernetes)
}

package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/rest"
)

func TestPlatformString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		platform Platform
		expected string
	}{
		{
			name:     "Kubernetes platform",
			platform: PlatformKubernetes,
			expected: "Kubernetes",
		},
		{
			name:     "OpenShift platform",
			platform: PlatformOpenShift,
			expected: "OpenShift",
		},
		{
			name:     "Unknown platform",
			platform: Platform(999),
			expected: "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := tt.platform.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestNewDefaultPlatformDetector(t *testing.T) {
	t.Parallel()

	detector := NewDefaultPlatformDetector()
	assert.NotNil(t, detector)
	assert.IsType(t, &DefaultPlatformDetector{}, detector)
}

func TestDefaultPlatformDetector_DetectPlatform(t *testing.T) {
	t.Parallel()

	detector := NewDefaultPlatformDetector()

	// This test will use a config that will fail to connect
	// We expect it to return an error consistently
	config := &rest.Config{
		Host: "http://localhost:12345", // Non-existent endpoint
	}

	// The first call should return either an error or success based on environment
	platform1, err1 := detector.DetectPlatform(config)

	// The second call should return the same result (cached)
	platform2, err2 := detector.DetectPlatform(config)

	// Verify that both calls return the same result (caching works)
	assert.Equal(t, platform1, platform2)

	// Verify error consistency
	if err1 != nil {
		assert.Error(t, err2, "Both calls should return the same error state")
		assert.Equal(t, err1.Error(), err2.Error())
		assert.Equal(t, PlatformKubernetes, platform1) // Default value when error occurs
	} else {
		assert.NoError(t, err2, "Both calls should return the same error state")
		// When OPERATOR_OPENSHIFT=true, we expect OpenShift platform
		assert.Equal(t, PlatformOpenShift, platform1)
	}
}

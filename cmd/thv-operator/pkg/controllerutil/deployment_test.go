package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppendProxyArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		baseArgs     []string
		overrideArgs []string
		expected     []string
	}{
		{
			name:         "append debug flag",
			baseArgs:     []string{"run", "image"},
			overrideArgs: []string{"--debug"},
			expected:     []string{"run", "image", "--debug"},
		},
		{
			name:         "append multiple args",
			baseArgs:     []string{"run", "image"},
			overrideArgs: []string{"--debug", "--verbose"},
			expected:     []string{"run", "image", "--debug", "--verbose"},
		},
		{
			name:         "empty override args returns base args unchanged",
			baseArgs:     []string{"run", "image"},
			overrideArgs: []string{},
			expected:     []string{"run", "image"},
		},
		{
			name:         "nil override args returns base args unchanged",
			baseArgs:     []string{"run", "image"},
			overrideArgs: nil,
			expected:     []string{"run", "image"},
		},
		{
			name:         "append with existing flags",
			baseArgs:     []string{"run", "--k8s-pod-patch={}", "image"},
			overrideArgs: []string{"--debug"},
			expected:     []string{"run", "--k8s-pod-patch={}", "image", "--debug"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := AppendProxyArgs(tt.baseArgs, tt.overrideArgs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

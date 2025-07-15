package updates

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewVersionClientForComponent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		component string
		version   string
		expected  string
	}{
		{
			name:      "CLI component",
			component: "CLI",
			version:   "",
			expected:  "CLI",
		},
		{
			name:      "operator component",
			component: "operator",
			version:   "",
			expected:  "operator",
		},
		{
			name:      "UI component with version",
			component: "UI",
			version:   "2.0.0",
			expected:  "UI",
		},
		{
			name:      "API component",
			component: "API",
			version:   "",
			expected:  "API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewVersionClientForComponent(tt.component, tt.version)
			defaultClient, ok := client.(*defaultVersionClient)
			assert.True(t, ok, "Expected defaultVersionClient type")
			assert.Equal(t, tt.expected, defaultClient.component)
			assert.Equal(t, tt.version, defaultClient.customVersion)
		})
	}
}

package updates

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewVersionClientForComponent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		component      string
		version        string
		uiReleaseBuild bool
		expected       string
	}{
		{
			name:           "CLI component",
			component:      "CLI",
			version:        "",
			uiReleaseBuild: false,
			expected:       "CLI",
		},
		{
			name:           "operator component",
			component:      "operator",
			version:        "",
			uiReleaseBuild: false,
			expected:       "operator",
		},
		{
			name:           "UI component with version and release build",
			component:      "UI",
			version:        "2.0.0",
			uiReleaseBuild: true,
			expected:       "UI",
		},
		{
			name:           "UI component with version and local build",
			component:      "UI",
			version:        "2.0.0",
			uiReleaseBuild: false,
			expected:       "UI",
		},
		{
			name:           "API component",
			component:      "API",
			version:        "",
			uiReleaseBuild: false,
			expected:       "API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewVersionClientForComponent(tt.component, tt.version, tt.uiReleaseBuild)
			defaultClient, ok := client.(*defaultVersionClient)
			assert.True(t, ok, "Expected defaultVersionClient type")
			assert.Equal(t, tt.expected, defaultClient.component)
			assert.Equal(t, tt.version, defaultClient.customVersion)
			assert.Equal(t, tt.uiReleaseBuild, defaultClient.uiReleaseBuild)
		})
	}
}

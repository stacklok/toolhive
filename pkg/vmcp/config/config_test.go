package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestOutgoingAuthConfig_ResolveForBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *OutgoingAuthConfig
		backendID    string
		wantStrategy string
		wantMetadata map[string]any
		description  string
	}{
		{
			name:         "nil config returns empty",
			config:       nil,
			backendID:    "backend1",
			wantStrategy: "",
			wantMetadata: nil,
			description:  "When config is nil, should return empty values",
		},
		{
			name: "backend-specific config takes precedence",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type:     "default-strategy",
					Metadata: map[string]any{"key": "default-value"},
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type:     "backend-specific-strategy",
						Metadata: map[string]any{"key": "backend-value"},
					},
				},
			},
			backendID:    "backend1",
			wantStrategy: "backend-specific-strategy",
			wantMetadata: map[string]any{"key": "backend-value"},
			description:  "Backend-specific config should override default",
		},
		{
			name: "falls back to default when backend not configured",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type:     "default-strategy",
					Metadata: map[string]any{"key": "default-value"},
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type:     "backend-specific-strategy",
						Metadata: map[string]any{"key": "backend-value"},
					},
				},
			},
			backendID:    "backend2",
			wantStrategy: "default-strategy",
			wantMetadata: map[string]any{"key": "default-value"},
			description:  "Should use default when specific backend not configured",
		},
		{
			name: "returns empty when no default and backend not configured",
			config: &OutgoingAuthConfig{
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type:     "backend-specific-strategy",
						Metadata: map[string]any{"key": "backend-value"},
					},
				},
			},
			backendID:    "backend2",
			wantStrategy: "",
			wantMetadata: nil,
			description:  "Should return empty when no default and backend not in map",
		},
		{
			name: "handles nil backend strategy in map",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type:     "default-strategy",
					Metadata: map[string]any{"key": "default-value"},
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": nil,
				},
			},
			backendID:    "backend1",
			wantStrategy: "default-strategy",
			wantMetadata: map[string]any{"key": "default-value"},
			description:  "Should fall back to default when backend strategy is nil",
		},
		{
			name: "returns empty when only default is nil",
			config: &OutgoingAuthConfig{
				Default:  nil,
				Backends: map[string]*authtypes.BackendAuthStrategy{},
			},
			backendID:    "backend1",
			wantStrategy: "",
			wantMetadata: nil,
			description:  "Should return empty when default is nil and backend not found",
		},
		{
			name: "handles strategy with nil metadata",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type:     "default-strategy",
					Metadata: nil,
				},
			},
			backendID:    "backend1",
			wantStrategy: "default-strategy",
			wantMetadata: nil,
			description:  "Should handle nil metadata correctly",
		},
		{
			name: "handles strategy with empty metadata",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type:     "default-strategy",
					Metadata: map[string]any{},
				},
			},
			backendID:    "backend1",
			wantStrategy: "default-strategy",
			wantMetadata: map[string]any{},
			description:  "Should return empty map when metadata is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStrategy, gotMetadata := tt.config.ResolveForBackend(tt.backendID)

			assert.Equal(t, tt.wantStrategy, gotStrategy, "Strategy mismatch: %s", tt.description)
			assert.Equal(t, tt.wantMetadata, gotMetadata, "Metadata mismatch: %s", tt.description)
		})
	}
}

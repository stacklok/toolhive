package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestOutgoingAuthConfig_ResolveForBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *OutgoingAuthConfig
		backendID   string
		wantType    string
		wantNil     bool
		description string
	}{
		{
			name:        "nil config returns nil",
			config:      nil,
			backendID:   "backend1",
			wantNil:     true,
			description: "When config is nil, should return nil",
		},
		{
			name: "backend-specific config takes precedence",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "X-API-Key",
							HeaderValue: "secret-token",
						},
					},
				},
			},
			backendID:   "backend1",
			wantType:    "header_injection",
			description: "Backend-specific config should override default",
		},
		{
			name: "falls back to default when backend not configured",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "Authorization",
							HeaderValue: "Bearer token123",
						},
					},
				},
			},
			backendID:   "backend2",
			wantType:    "unauthenticated",
			description: "Should use default when specific backend not configured",
		},
		{
			name: "returns nil when no default and backend not configured",
			config: &OutgoingAuthConfig{
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "X-Token",
							HeaderValue: "value123",
						},
					},
				},
			},
			backendID:   "backend2",
			wantNil:     true,
			description: "Should return nil when no default and backend not in map",
		},
		{
			name: "handles nil backend strategy in map",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": nil,
				},
			},
			backendID:   "backend1",
			wantType:    "unauthenticated",
			description: "Should fall back to default when backend strategy is nil",
		},
		{
			name: "returns nil when only default is nil",
			config: &OutgoingAuthConfig{
				Default:  nil,
				Backends: map[string]*authtypes.BackendAuthStrategy{},
			},
			backendID:   "backend1",
			wantNil:     true,
			description: "Should return nil when default is nil and backend not found",
		},
		{
			name: "handles header injection with env variable",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "header_injection",
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:     "Authorization",
						HeaderValueEnv: "API_KEY_ENV",
					},
				},
			},
			backendID:   "backend1",
			wantType:    "header_injection",
			description: "Should handle header injection with env variable",
		},
		{
			name: "handles token exchange strategy",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "token_exchange",
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL: "https://example.com/token",
						ClientID: "test-client",
						Audience: "api",
					},
				},
			},
			backendID:   "backend1",
			wantType:    "token_exchange",
			description: "Should handle token exchange strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.config.ResolveForBackend(tt.backendID)

			if tt.wantNil {
				assert.Nil(t, got, "Expected nil: %s", tt.description)
			} else {
				assert.NotNil(t, got, "Expected non-nil strategy: %s", tt.description)
				assert.Equal(t, tt.wantType, got.Type, "Type mismatch: %s", tt.description)
			}
		})
	}
}

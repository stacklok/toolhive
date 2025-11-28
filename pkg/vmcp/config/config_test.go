package config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestOutgoingAuthConfig_ResolveForBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *OutgoingAuthConfig
		backendID      string
		wantStrategy   *authtypes.BackendAuthStrategy
		description    string
	}{
		{
			name:           "nil config returns nil",
			config:         nil,
			backendID:      "backend1",
			wantStrategy:   nil,
			description:    "When config is nil, should return nil",
		},
		{
			name: "backend-specific config takes precedence",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "default-strategy",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "backend-specific-strategy",
					},
				},
			},
			backendID: "backend1",
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: "backend-specific-strategy",
			},
			description: "Backend-specific config should override default",
		},
		{
			name: "falls back to default when backend not configured",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "default-strategy",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "backend-specific-strategy",
					},
				},
			},
			backendID: "backend2",
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: "default-strategy",
			},
			description: "Should use default when specific backend not configured",
		},
		{
			name: "returns nil when no default and backend not configured",
			config: &OutgoingAuthConfig{
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": {
						Type: "backend-specific-strategy",
					},
				},
			},
			backendID:    "backend2",
			wantStrategy: nil,
			description:  "Should return nil when no default and backend not in map",
		},
		{
			name: "handles nil backend strategy in map",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: "default-strategy",
				},
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"backend1": nil,
				},
			},
			backendID: "backend1",
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: "default-strategy",
			},
			description: "Should fall back to default when backend strategy is nil",
		},
		{
			name: "returns nil when only default is nil",
			config: &OutgoingAuthConfig{
				Default:  nil,
				Backends: map[string]*authtypes.BackendAuthStrategy{},
			},
			backendID:    "backend1",
			wantStrategy: nil,
			description:  "Should return nil when default is nil and backend not found",
		},
		{
			name: "handles header injection strategy",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeHeaderInjection,
					HeaderInjection: &authtypes.HeaderInjectionConfig{
						HeaderName:  "X-API-Key",
						HeaderValue: "test-value",
					},
				},
			},
			backendID: "backend1",
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "X-API-Key",
					HeaderValue: "test-value",
				},
			},
			description: "Should return full header injection strategy",
		},
		{
			name: "handles token exchange strategy",
			config: &OutgoingAuthConfig{
				Default: &authtypes.BackendAuthStrategy{
					Type: authtypes.StrategyTypeTokenExchange,
					TokenExchange: &authtypes.TokenExchangeConfig{
						TokenURL: "https://example.com/token",
						ClientID: "client-id",
					},
				},
			},
			backendID: "backend1",
			wantStrategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeTokenExchange,
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL: "https://example.com/token",
					ClientID: "client-id",
				},
			},
			description: "Should return full token exchange strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStrategy := tt.config.ResolveForBackend(tt.backendID)

			assert.Equal(t, tt.wantStrategy, gotStrategy, "Strategy mismatch: %s", tt.description)
		})
	}
}

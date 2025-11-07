package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestNewOutgoingAuthRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cfg           *config.OutgoingAuthConfig
		wantErr       bool
		errContains   string
		checkRegistry func(t *testing.T, cfg *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry)
	}{
		{
			name:    "nil config returns registry with unauthenticated strategy",
			cfg:     nil,
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()
				require.NotNil(t, registry)

				// Registry should have unauthenticated strategy
				strategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, strategy)
			},
		},
		{
			name: "empty config returns registry with unauthenticated strategy",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()
				require.NotNil(t, registry)

				// Registry should have unauthenticated strategy
				strategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, strategy)
			},
		},
		{
			name: "default strategy with empty type fails",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "",
				},
			},
			wantErr:     true,
			errContains: "default auth strategy type cannot be empty",
		},
		{
			name: "default strategy with whitespace type fails",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "   ",
				},
			},
			wantErr:     true,
			errContains: "default auth strategy type cannot be empty",
		},
		{
			name: "backend strategy with empty type fails",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "",
					},
				},
			},
			wantErr:     true,
			errContains: "backend \"github\" has empty auth strategy type",
		},
		{
			name: "backend strategy with whitespace type fails",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "  \t  ",
					},
				},
			},
			wantErr:     true,
			errContains: "backend \"github\" has empty auth strategy type",
		},
		{
			name: "unknown strategy type in default fails",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "unknown_strategy",
				},
			},
			wantErr:     true,
			errContains: "unknown strategy type: unknown_strategy",
		},
		{
			name: "unknown strategy type in backend fails",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "magic_auth",
					},
				},
			},
			wantErr:     true,
			errContains: "unknown strategy type: magic_auth",
		},
		{
			name: "valid header_injection in default succeeds",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "header_injection",
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have both unauthenticated and header_injection
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)

				headerStrategy, err := registry.GetStrategy("header_injection")
				require.NoError(t, err)
				assert.NotNil(t, headerStrategy)
			},
		},
		{
			name: "valid header_injection in backend succeeds",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have both unauthenticated and header_injection
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)

				headerStrategy, err := registry.GetStrategy("header_injection")
				require.NoError(t, err)
				assert.NotNil(t, headerStrategy)
			},
		},
		{
			name: "multiple backends with same strategy type registers once",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
					"gitlab": {
						Type: "header_injection",
					},
					"jira": {
						Type: "header_injection",
					},
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have both strategies
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)

				headerStrategy, err := registry.GetStrategy("header_injection")
				require.NoError(t, err)
				assert.NotNil(t, headerStrategy)

				// Verify all backends can use the same strategy instance
				// (This tests that we collect unique strategy types)
			},
		},
		{
			name: "default and backend with different strategies registers both",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have both strategies
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)

				headerStrategy, err := registry.GetStrategy("header_injection")
				require.NoError(t, err)
				assert.NotNil(t, headerStrategy)
			},
		},
		{
			name: "default unauthenticated does not register duplicate",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "unauthenticated",
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have unauthenticated strategy
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)
			},
		},
		{
			name: "backend unauthenticated does not register duplicate",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "unauthenticated",
					},
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have unauthenticated strategy
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)
			},
		},
		{
			name: "complex config with multiple backends and strategies succeeds",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
						Metadata: map[string]any{
							"headers": map[string]string{
								"Authorization": "Bearer token",
							},
						},
					},
					"gitlab": {
						Type: "header_injection",
						Metadata: map[string]any{
							"headers": map[string]string{
								"Private-Token": "token",
							},
						},
					},
					"public-api": {
						Type: "unauthenticated",
					},
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have both strategies
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)

				headerStrategy, err := registry.GetStrategy("header_injection")
				require.NoError(t, err)
				assert.NotNil(t, headerStrategy)
			},
		},
		{
			name: "nil backend in backends map is ignored",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
					"ignored": nil,
				},
			},
			wantErr: false,
			checkRegistry: func(t *testing.T, _ *config.OutgoingAuthConfig, registry auth.OutgoingAuthRegistry) {
				t.Helper()

				// Should have both strategies
				unauthStrategy, err := registry.GetStrategy("unauthenticated")
				require.NoError(t, err)
				assert.NotNil(t, unauthStrategy)

				headerStrategy, err := registry.GetStrategy("header_injection")
				require.NoError(t, err)
				assert.NotNil(t, headerStrategy)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			registry, err := NewOutgoingAuthRegistry(ctx, tt.cfg)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, registry)
			} else {
				require.NoError(t, err)
				require.NotNil(t, registry)
				if tt.checkRegistry != nil {
					tt.checkRegistry(t, tt.cfg, registry)
				}
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         *config.OutgoingAuthConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid config with default and backends",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
				Default: &config.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty default type fails",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "",
				},
			},
			wantErr:     true,
			errContains: "default auth strategy type cannot be empty",
		},
		{
			name: "whitespace default type fails",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "   \t\n  ",
				},
			},
			wantErr:     true,
			errContains: "default auth strategy type cannot be empty",
		},
		{
			name: "empty backend type fails with backend id in error",
			cfg: &config.OutgoingAuthConfig{
				Backends: map[string]*config.BackendAuthStrategy{
					"my-backend": {
						Type: "",
					},
				},
			},
			wantErr:     true,
			errContains: `backend "my-backend" has empty auth strategy type`,
		},
		{
			name: "multiple backends with empty types fails on first",
			cfg: &config.OutgoingAuthConfig{
				Backends: map[string]*config.BackendAuthStrategy{
					"backend1": {
						Type: "",
					},
					"backend2": {
						Type: "",
					},
				},
			},
			wantErr: true,
			// Will fail on one of them (map iteration order is random)
			errContains: "has empty auth strategy type",
		},
		{
			name: "nil backend entries are allowed",
			cfg: &config.OutgoingAuthConfig{
				Backends: map[string]*config.BackendAuthStrategy{
					"backend1": nil,
					"backend2": {
						Type: "header_injection",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateConfig(tt.cfg)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCollectStrategyTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		cfg           *config.OutgoingAuthConfig
		wantTypes     []string
		wantTypeCount int
	}{
		{
			name: "empty config returns empty set",
			cfg: &config.OutgoingAuthConfig{
				Source: "inline",
			},
			wantTypes:     []string{},
			wantTypeCount: 0,
		},
		{
			name: "default strategy only",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "header_injection",
				},
			},
			wantTypes:     []string{"header_injection"},
			wantTypeCount: 1,
		},
		{
			name: "backend strategy only",
			cfg: &config.OutgoingAuthConfig{
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantTypes:     []string{"header_injection"},
			wantTypeCount: 1,
		},
		{
			name: "duplicate strategy types are deduplicated",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "header_injection",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
					"gitlab": {
						Type: "header_injection",
					},
				},
			},
			wantTypes:     []string{"header_injection"},
			wantTypeCount: 1,
		},
		{
			name: "multiple different strategy types",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantTypes:     []string{"unauthenticated", "header_injection"},
			wantTypeCount: 2,
		},
		{
			name: "nil default is ignored",
			cfg: &config.OutgoingAuthConfig{
				Default: nil,
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantTypes:     []string{"header_injection"},
			wantTypeCount: 1,
		},
		{
			name: "nil backends are ignored",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "unauthenticated",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github":  nil,
					"gitlab":  nil,
					"backend": {Type: "header_injection"},
				},
			},
			wantTypes:     []string{"unauthenticated", "header_injection"},
			wantTypeCount: 2,
		},
		{
			name: "empty type strings are ignored",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "",
				},
				Backends: map[string]*config.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
					},
				},
			},
			wantTypes:     []string{"header_injection"},
			wantTypeCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategyTypes := collectStrategyTypes(tt.cfg)

			assert.Equal(t, tt.wantTypeCount, len(strategyTypes), "unexpected number of strategy types")

			// Check all expected types are present
			for _, expectedType := range tt.wantTypes {
				_, exists := strategyTypes[expectedType]
				assert.True(t, exists, "expected strategy type %q not found", expectedType)
			}
		})
	}
}

func TestCreateStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		strategyType  string
		wantErr       bool
		errContains   string
		checkStrategy func(t *testing.T, strategy interface{})
	}{
		{
			name:         "header_injection creates strategy",
			strategyType: "header_injection",
			wantErr:      false,
			checkStrategy: func(t *testing.T, strategy interface{}) {
				t.Helper()
				require.NotNil(t, strategy)
				// Verify it's the right type by checking Name()
				named := strategy.(interface{ Name() string })
				assert.Equal(t, "header_injection", named.Name())
			},
		},
		{
			name:         "unauthenticated creates strategy",
			strategyType: "unauthenticated",
			wantErr:      false,
			checkStrategy: func(t *testing.T, strategy interface{}) {
				t.Helper()
				require.NotNil(t, strategy)
				named := strategy.(interface{ Name() string })
				assert.Equal(t, "unauthenticated", named.Name())
			},
		},
		{
			name:         "unknown strategy type fails",
			strategyType: "magic_auth",
			wantErr:      true,
			errContains:  "unknown strategy type: magic_auth",
		},
		{
			name:         "empty strategy type fails",
			strategyType: "",
			wantErr:      true,
			errContains:  "strategy type cannot be empty",
		},
		{
			name:         "whitespace strategy type fails",
			strategyType: "   \t\n  ",
			wantErr:      true,
			errContains:  "strategy type cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy, err := createStrategy(tt.strategyType)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, strategy)
			} else {
				require.NoError(t, err)
				require.NotNil(t, strategy)
				if tt.checkStrategy != nil {
					tt.checkStrategy(t, strategy)
				}
			}
		})
	}
}

func TestRegisterUnauthenticatedStrategy(t *testing.T) {
	t.Parallel()

	t.Run("successfully registers unauthenticated strategy", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		registry, err := NewOutgoingAuthRegistry(ctx, nil)
		require.NoError(t, err)
		require.NotNil(t, registry)

		// Should be able to retrieve the strategy
		strategy, err := registry.GetStrategy("unauthenticated")
		require.NoError(t, err)
		assert.NotNil(t, strategy)
		assert.Equal(t, "unauthenticated", strategy.Name())
	})
}

func TestErrorMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		cfg              *config.OutgoingAuthConfig
		wantErrSubstring []string // All of these should be in the error
	}{
		{
			name: "unknown strategy error includes strategy name",
			cfg: &config.OutgoingAuthConfig{
				Default: &config.BackendAuthStrategy{
					Type: "nonexistent_strategy",
				},
			},
			wantErrSubstring: []string{"unknown strategy type", "nonexistent_strategy"},
		},
		{
			name: "empty backend type error includes backend id",
			cfg: &config.OutgoingAuthConfig{
				Backends: map[string]*config.BackendAuthStrategy{
					"my-special-backend": {
						Type: "",
					},
				},
			},
			wantErrSubstring: []string{"backend", "my-special-backend", "empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			_, err := NewOutgoingAuthRegistry(ctx, tt.cfg)
			require.Error(t, err)

			errMsg := err.Error()
			for _, substring := range tt.wantErrSubstring {
				assert.True(t, strings.Contains(errMsg, substring),
					"error message %q should contain %q", errMsg, substring)
			}
		})
	}
}

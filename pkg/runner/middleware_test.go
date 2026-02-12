// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/upstreamswap"
	"github.com/stacklok/toolhive/pkg/authserver"
	headerfwd "github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// createMinimalAuthServerConfig creates a minimal valid EmbeddedAuthServerConfig for testing.
func createMinimalAuthServerConfig() *authserver.RunConfig {
	return &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        "http://localhost:8080",
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "test-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					AuthorizationEndpoint: "https://example.com/authorize",
					TokenEndpoint:         "https://example.com/token",
					ClientID:              "test-client-id",
					RedirectURI:           "http://localhost:8080/oauth/callback",
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}
}

func TestAddHeaderForwardMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *RunConfig
		wantAppended  bool
		wantErrSubstr string
	}{
		{
			name:         "empty RemoteURL returns input unchanged",
			config:       &RunConfig{RemoteURL: "", HeaderForward: &HeaderForwardConfig{AddPlaintextHeaders: map[string]string{"X-Key": "val"}}},
			wantAppended: false,
		},
		{
			name:         "nil HeaderForward returns input unchanged",
			config:       &RunConfig{RemoteURL: "https://example.com", HeaderForward: nil},
			wantAppended: false,
		},
		{
			name:         "HasHeaders false returns input unchanged",
			config:       &RunConfig{RemoteURL: "https://example.com", HeaderForward: &HeaderForwardConfig{}},
			wantAppended: false,
		},
		{
			name: "valid config appends middleware with correct type and params",
			config: &RunConfig{
				RemoteURL: "https://example.com",
				HeaderForward: &HeaderForwardConfig{
					AddPlaintextHeaders: map[string]string{"Authorization": "Bearer tok", "X-Custom": "value"},
				},
			},
			wantAppended: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			initial := []types.MiddlewareConfig{{Type: "existing"}}
			got, err := addHeaderForwardMiddleware(initial, tt.config)

			if tt.wantErrSubstr != "" {
				require.ErrorContains(t, err, tt.wantErrSubstr)
				return
			}
			require.NoError(t, err)

			if !tt.wantAppended {
				assert.Equal(t, initial, got, "middleware slice should be unchanged")
				return
			}

			// Should have one additional entry.
			require.Len(t, got, len(initial)+1)
			added := got[len(got)-1]
			assert.Equal(t, headerfwd.HeaderForwardMiddlewareName, added.Type)

			// Verify serialized params contain the headers.
			var params headerfwd.HeaderForwardMiddlewareParams
			require.NoError(t, json.Unmarshal(added.Parameters, &params))
			assert.Equal(t, tt.config.HeaderForward.ResolvedHeaders(), params.AddHeaders)
		})
	}
}

func TestPopulateMiddlewareConfigs_HeaderForward(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		config            *RunConfig
		wantHeaderForward bool
	}{
		{
			name: "RemoteURL with headers includes header-forward",
			config: &RunConfig{
				RemoteURL: "https://example.com",
				HeaderForward: &HeaderForwardConfig{
					AddPlaintextHeaders: map[string]string{"X-Key": "val"},
				},
			},
			wantHeaderForward: true,
		},
		{
			name: "no RemoteURL omits header-forward",
			config: &RunConfig{
				RemoteURL: "",
				HeaderForward: &HeaderForwardConfig{
					AddPlaintextHeaders: map[string]string{"X-Key": "val"},
				},
			},
			wantHeaderForward: false,
		},
		{
			name: "RemoteURL with nil HeaderForward omits header-forward",
			config: &RunConfig{
				RemoteURL:     "https://example.com",
				HeaderForward: nil,
			},
			wantHeaderForward: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := PopulateMiddlewareConfigs(tt.config)
			require.NoError(t, err)

			found := false
			for _, mw := range tt.config.MiddlewareConfigs {
				if mw.Type == headerfwd.HeaderForwardMiddlewareName {
					found = true
					break
				}
			}
			assert.Equal(t, tt.wantHeaderForward, found,
				"header-forward middleware presence mismatch")
		})
	}
}

// TestWithMiddlewareFromFlags_ExcludesHeaderForward verifies that WithMiddlewareFromFlags
// does NOT add header-forward middleware. Header forward is added later in Runner.Run()
// after secret resolution, because secret-backed header values are unavailable at builder time.
func TestWithMiddlewareFromFlags_ExcludesHeaderForward(t *testing.T) {
	t.Parallel()

	opts := []RunConfigBuilderOption{
		WithName("test"),
		WithTransportAndPorts("streamable-http", 0, 0),
		WithRemoteURL("http://example.com"),
		WithHeaderForward(map[string]string{"X-Key": "val"}),
		WithMiddlewareFromFlags(
			nil, nil, nil, nil, nil, "", false, "", "", "", false,
		),
	}

	cfg, err := NewRunConfigBuilder(t.Context(), nil, nil, nil, opts...)
	require.NoError(t, err)

	for _, mw := range cfg.MiddlewareConfigs {
		assert.NotEqual(t, headerfwd.HeaderForwardMiddlewareName, mw.Type,
			"header-forward should not be added by WithMiddlewareFromFlags")
	}

	// Verify HeaderForward config is set (used by Runner.Run after secret resolution)
	require.NotNil(t, cfg.HeaderForward)
	assert.Equal(t, map[string]string{"X-Key": "val"}, cfg.HeaderForward.AddPlaintextHeaders)
}

func TestGetSupportedMiddlewareFactories(t *testing.T) {
	t.Parallel()

	factories := GetSupportedMiddlewareFactories()
	for _, key := range []string{
		headerfwd.HeaderForwardMiddlewareName,
		upstreamswap.MiddlewareType,
		awssts.MiddlewareType,
	} {
		_, ok := factories[key]
		assert.True(t, ok, "factory map should contain %q", key)
	}
}

func TestWithHeaderForwardSecretsBuilderOption(t *testing.T) {
	t.Parallel()

	baseOpts := []RunConfigBuilderOption{
		WithName("test"),
		WithTransportAndPorts("streamable-http", 0, 0),
	}

	t.Run("populates AddHeadersFromSecret", func(t *testing.T) {
		t.Parallel()
		opts := append(baseOpts, WithHeaderForwardSecrets(map[string]string{"Authorization": "auth-secret", "X-Token": "tok-secret"}))
		cfg, err := NewRunConfigBuilder(t.Context(), nil, nil, nil, opts...)
		require.NoError(t, err)
		require.NotNil(t, cfg.HeaderForward)
		assert.Equal(t, map[string]string{"Authorization": "auth-secret", "X-Token": "tok-secret"}, cfg.HeaderForward.AddHeadersFromSecret)
	})

	t.Run("nil and empty are no-ops", func(t *testing.T) {
		t.Parallel()
		for _, input := range []map[string]string{nil, {}} {
			opts := append(baseOpts, WithHeaderForwardSecrets(input))
			cfg, err := NewRunConfigBuilder(t.Context(), nil, nil, nil, opts...)
			require.NoError(t, err)
			assert.Nil(t, cfg.HeaderForward)
		}
	})

	t.Run("composes with WithHeaderForward", func(t *testing.T) {
		t.Parallel()
		opts := append(baseOpts,
			WithHeaderForward(map[string]string{"X-Static": "val"}),
			WithHeaderForwardSecrets(map[string]string{"X-Secret": "my-secret"}),
		)
		cfg, err := NewRunConfigBuilder(t.Context(), nil, nil, nil, opts...)
		require.NoError(t, err)
		require.NotNil(t, cfg.HeaderForward)
		assert.Equal(t, map[string]string{"X-Static": "val"}, cfg.HeaderForward.AddPlaintextHeaders)
		assert.Equal(t, map[string]string{"X-Secret": "my-secret"}, cfg.HeaderForward.AddHeadersFromSecret)
	})
}

func TestAddUpstreamSwapMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *RunConfig
		wantAppended bool
	}{
		{
			name:         "nil EmbeddedAuthServerConfig returns input unchanged",
			config:       &RunConfig{EmbeddedAuthServerConfig: nil},
			wantAppended: false,
		},
		{
			name: "EmbeddedAuthServerConfig set with nil UpstreamSwapConfig uses defaults",
			config: &RunConfig{
				EmbeddedAuthServerConfig: createMinimalAuthServerConfig(),
				UpstreamSwapConfig:       nil,
			},
			wantAppended: true,
		},
		{
			name: "EmbeddedAuthServerConfig set with explicit UpstreamSwapConfig uses provided config",
			config: &RunConfig{
				EmbeddedAuthServerConfig: createMinimalAuthServerConfig(),
				UpstreamSwapConfig: &upstreamswap.Config{
					HeaderStrategy: upstreamswap.HeaderStrategyReplace,
				},
			},
			wantAppended: true,
		},
		{
			name: "EmbeddedAuthServerConfig with custom header strategy config",
			config: &RunConfig{
				EmbeddedAuthServerConfig: createMinimalAuthServerConfig(),
				UpstreamSwapConfig: &upstreamswap.Config{
					HeaderStrategy:   upstreamswap.HeaderStrategyCustom,
					CustomHeaderName: "X-Upstream-Token",
				},
			},
			wantAppended: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			initial := []types.MiddlewareConfig{{Type: "existing"}}
			got, err := addUpstreamSwapMiddleware(initial, tt.config)
			require.NoError(t, err)

			if !tt.wantAppended {
				assert.Equal(t, initial, got, "middleware slice should be unchanged")
				return
			}

			// Should have one additional entry.
			require.Len(t, got, len(initial)+1)
			added := got[len(got)-1]
			assert.Equal(t, upstreamswap.MiddlewareType, added.Type)

			// Verify serialized params contain the expected config.
			var params upstreamswap.MiddlewareParams
			require.NoError(t, json.Unmarshal(added.Parameters, &params))

			if tt.config.UpstreamSwapConfig != nil {
				// Should use the provided config
				require.NotNil(t, params.Config)
				assert.Equal(t, tt.config.UpstreamSwapConfig.HeaderStrategy, params.Config.HeaderStrategy)
				assert.Equal(t, tt.config.UpstreamSwapConfig.CustomHeaderName, params.Config.CustomHeaderName)
			} else {
				// Should use defaults (empty config is valid)
				require.NotNil(t, params.Config)
			}
		})
	}
}

func TestPopulateMiddlewareConfigs_UpstreamSwap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		config             *RunConfig
		wantUpstreamSwap   bool
		wantHeaderStrategy string
	}{
		{
			name:             "EmbeddedAuthServerConfig set includes upstream-swap",
			config:           &RunConfig{EmbeddedAuthServerConfig: createMinimalAuthServerConfig()},
			wantUpstreamSwap: true,
		},
		{
			name:             "no EmbeddedAuthServerConfig omits upstream-swap",
			config:           &RunConfig{EmbeddedAuthServerConfig: nil},
			wantUpstreamSwap: false,
		},
		{
			name: "explicit UpstreamSwapConfig is used",
			config: &RunConfig{
				EmbeddedAuthServerConfig: createMinimalAuthServerConfig(),
				UpstreamSwapConfig: &upstreamswap.Config{
					HeaderStrategy: upstreamswap.HeaderStrategyReplace,
				},
			},
			wantUpstreamSwap:   true,
			wantHeaderStrategy: upstreamswap.HeaderStrategyReplace,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := PopulateMiddlewareConfigs(tt.config)
			require.NoError(t, err)

			var found bool
			var foundConfig *types.MiddlewareConfig
			for i, mw := range tt.config.MiddlewareConfigs {
				if mw.Type == upstreamswap.MiddlewareType {
					found = true
					foundConfig = &tt.config.MiddlewareConfigs[i]
					break
				}
			}
			assert.Equal(t, tt.wantUpstreamSwap, found,
				"upstream-swap middleware presence mismatch")

			// Verify config values if we expect the middleware and have specific expectations
			if found && tt.wantHeaderStrategy != "" {
				var params upstreamswap.MiddlewareParams
				require.NoError(t, json.Unmarshal(foundConfig.Parameters, &params))
				require.NotNil(t, params.Config)
				assert.Equal(t, tt.wantHeaderStrategy, params.Config.HeaderStrategy)
			}
		})
	}
}

func TestAddAWSStsMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *RunConfig
		wantAppended  bool
		wantErrSubstr string
	}{
		{
			name:         "nil AWSStsConfig returns input unchanged",
			config:       &RunConfig{AWSStsConfig: nil},
			wantAppended: false,
		},
		{
			name: "valid AWSStsConfig appends middleware with correct type and params",
			config: &RunConfig{
				AWSStsConfig: &awssts.Config{
					Region:          "us-east-1",
					FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
				},
				RemoteURL: "https://aws-mcp.us-east-1.api.aws",
			},
			wantAppended: true,
		},
		{
			name: "AWSStsConfig without RemoteURL returns error",
			config: &RunConfig{
				AWSStsConfig: &awssts.Config{
					Region:          "us-east-1",
					FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
				},
			},
			wantErrSubstr: "AWS STS middleware requires a remote URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			initial := []types.MiddlewareConfig{{Type: "existing"}}
			got, err := addAWSStsMiddleware(initial, tt.config)

			if tt.wantErrSubstr != "" {
				require.ErrorContains(t, err, tt.wantErrSubstr)
				return
			}
			require.NoError(t, err)

			if !tt.wantAppended {
				assert.Equal(t, initial, got, "middleware slice should be unchanged")
				return
			}

			require.Len(t, got, len(initial)+1)
			added := got[len(got)-1]
			assert.Equal(t, awssts.MiddlewareType, added.Type)

			// Verify serialized params contain the config and target URL.
			var params awssts.MiddlewareParams
			require.NoError(t, json.Unmarshal(added.Parameters, &params))
			require.NotNil(t, params.AWSStsConfig)
			assert.Equal(t, tt.config.AWSStsConfig.Region, params.AWSStsConfig.Region)
			assert.Equal(t, tt.config.RemoteURL, params.TargetURL)
		})
	}
}

func TestPopulateMiddlewareConfigs_AWSSts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *RunConfig
		wantAWSSts    bool
		wantErrSubstr string
	}{
		{
			name: "AWSStsConfig with RemoteURL includes awssts middleware",
			config: &RunConfig{
				AWSStsConfig: &awssts.Config{
					Region:          "us-east-1",
					FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
				},
				RemoteURL: "https://aws-mcp.us-east-1.api.aws",
			},
			wantAWSSts: true,
		},
		{
			name:       "nil AWSStsConfig omits awssts middleware",
			config:     &RunConfig{AWSStsConfig: nil},
			wantAWSSts: false,
		},
		{
			name: "AWSStsConfig without RemoteURL returns error",
			config: &RunConfig{
				AWSStsConfig: &awssts.Config{
					Region:          "us-east-1",
					FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
				},
			},
			wantErrSubstr: "AWS STS middleware requires a remote URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := PopulateMiddlewareConfigs(tt.config)

			if tt.wantErrSubstr != "" {
				require.ErrorContains(t, err, tt.wantErrSubstr)
				return
			}
			require.NoError(t, err)

			found := false
			for _, mw := range tt.config.MiddlewareConfigs {
				if mw.Type == awssts.MiddlewareType {
					found = true
					break
				}
			}
			assert.Equal(t, tt.wantAWSSts, found,
				"awssts middleware presence mismatch")
		})
	}
}

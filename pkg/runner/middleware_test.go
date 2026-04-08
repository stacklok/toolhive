// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/upstreamswap"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/recovery"
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

// TestPopulateMiddlewareConfigs_AWSStsOrdering verifies that when AWS STS
// middleware is present it appears after authz/audit and before header-forward
// and recovery in the middleware chain. SigV4 signing must happen late in the
// chain so that earlier middleware (authz, audit) can reject requests before
// credentials are exchanged, and later middleware (header-forward) does not
// mutate headers after signing.
func TestPopulateMiddlewareConfigs_AWSStsOrdering(t *testing.T) {
	t.Parallel()

	config := &RunConfig{
		AWSStsConfig: &awssts.Config{
			Region:          "us-east-1",
			FallbackRoleArn: "arn:aws:iam::123456789012:role/TestRole",
		},
		RemoteURL: "https://aws-mcp.us-east-1.api.aws",
		HeaderForward: &HeaderForwardConfig{
			AddPlaintextHeaders: map[string]string{"X-Key": "val"},
		},
	}

	err := PopulateMiddlewareConfigs(config)
	require.NoError(t, err)

	// Build a type→index map for easy position comparison.
	typeIndex := make(map[string]int, len(config.MiddlewareConfigs))
	for i, mw := range config.MiddlewareConfigs {
		typeIndex[mw.Type] = i
	}

	awsStsIdx, ok := typeIndex[awssts.MiddlewareType]
	require.True(t, ok, "awssts middleware should be present")

	// AWS STS must come after auth (innermost auth check).
	authIdx, ok := typeIndex[auth.MiddlewareType]
	require.True(t, ok, "auth middleware should be present")
	assert.Greater(t, awsStsIdx, authIdx,
		"awssts must appear after auth middleware")

	// AWS STS must come before header-forward so signing isn't invalidated.
	hfIdx, ok := typeIndex[headerfwd.HeaderForwardMiddlewareName]
	require.True(t, ok, "header-forward middleware should be present")
	assert.Less(t, awsStsIdx, hfIdx,
		"awssts must appear before header-forward middleware")

	// AWS STS must come before recovery (outermost wrapper).
	recoveryIdx, ok := typeIndex[recovery.MiddlewareType]
	require.True(t, ok, "recovery middleware should be present")
	assert.Less(t, awsStsIdx, recoveryIdx,
		"awssts must appear before recovery middleware")
}

// makeCedarAuthzConfig is a helper that creates a valid Cedar authz.Config.
func makeCedarAuthzConfig(t *testing.T) *authz.Config {
	t.Helper()
	cfg, err := authorizers.NewConfig(cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     []string{`permit(principal, action, resource);`},
			EntitiesJSON: "[]",
		},
	})
	require.NoError(t, err)
	return cfg
}

// TestInjectUpstreamProviderIfNeeded tests the injectUpstreamProviderIfNeeded helper.
func TestInjectUpstreamProviderIfNeeded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		authzCfg         *authz.Config
		embeddedCfg      *authserver.RunConfig
		wantErr          bool
		wantProviderName string
		wantSamePointer  bool
	}{
		{
			name:            "nil_embedded_config_returns_authz_unchanged",
			authzCfg:        nil,
			embeddedCfg:     nil,
			wantErr:         false,
			wantSamePointer: true, // returns authzCfg unchanged (nil == nil)
		},
		{
			name:            "non_nil_authz_nil_embedded_returns_unchanged",
			authzCfg:        nil, // set in-test
			embeddedCfg:     nil,
			wantErr:         false,
			wantSamePointer: true,
		},
		{
			name: "named_upstream_is_used_as_provider",
			embeddedCfg: &authserver.RunConfig{
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "github"},
				},
			},
			wantErr:          false,
			wantProviderName: "github",
		},
		{
			name: "unnamed_upstream_falls_back_to_default",
			embeddedCfg: &authserver.RunConfig{
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: ""},
				},
			},
			wantErr:          false,
			wantProviderName: authserver.DefaultUpstreamName,
		},
		{
			name: "empty_upstreams_falls_back_to_default",
			embeddedCfg: &authserver.RunConfig{
				Upstreams: []authserver.UpstreamRunConfig{},
			},
			wantErr:          false,
			wantProviderName: authserver.DefaultUpstreamName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Build a real Cedar authz.Config if the test case didn't override it.
			authzCfg := tt.authzCfg
			if authzCfg == nil && !tt.wantSamePointer {
				authzCfg = makeCedarAuthzConfig(t)
			}
			if tt.name == "non_nil_authz_nil_embedded_returns_unchanged" {
				authzCfg = makeCedarAuthzConfig(t)
			}

			result, err := injectUpstreamProviderIfNeeded(authzCfg, tt.embeddedCfg)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			if tt.wantSamePointer {
				assert.Same(t, authzCfg, result)
				return
			}

			require.NotNil(t, result)

			// Verify the injected provider name is in the Cedar config.
			extracted, err := cedar.ExtractConfig(result)
			require.NoError(t, err)
			assert.Equal(t, tt.wantProviderName, extracted.Options.PrimaryUpstreamProvider)
		})
	}
}

// writeCedarConfigFile writes a minimal Cedar authorization config JSON file to a
// temporary directory and returns the path. The file is suitable for use as the
// authzConfigPath argument to addAuthzMiddleware.
func writeCedarConfigFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "authz.json")
	content := `{
		"version": "1.0",
		"type": "cedarv1",
		"cedar": {
			"policies": ["permit(principal, action, resource);"],
			"entities_json": "[]"
		}
	}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

func TestAddAuthzMiddleware_InjectsUpstreamProvider(t *testing.T) {
	t.Parallel()

	embeddedCfg := &authserver.RunConfig{
		Upstreams: []authserver.UpstreamRunConfig{
			{Name: "myidp"},
		},
	}

	path := writeCedarConfigFile(t)
	result, err := addAuthzMiddleware(nil, path, embeddedCfg)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.Equal(t, authz.MiddlewareType, result[0].Type)

	// Decode the params and verify the upstream provider was injected.
	var params authz.FactoryMiddlewareParams
	require.NoError(t, json.Unmarshal(result[0].Parameters, &params))
	require.NotNil(t, params.ConfigData, "ConfigData should be populated from file")

	extracted, err := cedar.ExtractConfig(params.ConfigData)
	require.NoError(t, err)
	assert.Equal(t, "myidp", extracted.Options.PrimaryUpstreamProvider)
}

func TestAddAuthzMiddleware_EmptyPath(t *testing.T) {
	t.Parallel()

	result, err := addAuthzMiddleware(nil, "", nil)
	require.NoError(t, err)
	assert.Empty(t, result, "empty path should produce no middleware")
}

func TestAddRateLimitMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *RunConfig
		wantAppended bool
		wantErr      bool
	}{
		{
			name:         "nil RateLimitConfig returns input unchanged",
			config:       &RunConfig{},
			wantAppended: false,
		},
		{
			name: "rate limit without Redis returns error",
			config: &RunConfig{
				RateLimitConfig: &v1alpha1.RateLimitConfig{
					Shared: &v1alpha1.RateLimitBucket{
						MaxTokens:    10,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "valid config appends middleware",
			config: &RunConfig{
				Name:               "test-server",
				RateLimitNamespace: "default",
				RateLimitConfig: &v1alpha1.RateLimitConfig{
					Shared: &v1alpha1.RateLimitBucket{
						MaxTokens:    10,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
				ScalingConfig: &ScalingConfig{
					SessionRedis: &SessionRedisConfig{
						Address: "redis:6379",
						DB:      0,
					},
				},
			},
			wantAppended: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			initial := []types.MiddlewareConfig{{Type: "existing"}}
			got, err := addRateLimitMiddleware(initial, tt.config)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "sessionStorage")
				return
			}
			require.NoError(t, err)

			if !tt.wantAppended {
				assert.Equal(t, initial, got)
				return
			}

			require.Len(t, got, len(initial)+1)
			added := got[len(got)-1]
			assert.Equal(t, ratelimit.MiddlewareType, added.Type)

			var params ratelimit.MiddlewareParams
			require.NoError(t, json.Unmarshal(added.Parameters, &params))
			assert.Equal(t, "default", params.Namespace)
			assert.Equal(t, "test-server", params.ServerName)
			assert.Equal(t, "redis:6379", params.RedisAddr)
			assert.NotNil(t, params.Config)
		})
	}
}

func TestPopulateMiddlewareConfigs_RateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		config        *RunConfig
		wantRateLimit bool
	}{
		{
			name: "rate limit config present includes middleware",
			config: &RunConfig{
				Name:               "test-server",
				RateLimitNamespace: "default",
				RateLimitConfig: &v1alpha1.RateLimitConfig{
					Shared: &v1alpha1.RateLimitBucket{
						MaxTokens:    5,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
				ScalingConfig: &ScalingConfig{
					SessionRedis: &SessionRedisConfig{Address: "redis:6379"},
				},
			},
			wantRateLimit: true,
		},
		{
			name:          "nil rate limit config omits middleware",
			config:        &RunConfig{},
			wantRateLimit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := PopulateMiddlewareConfigs(tt.config)
			require.NoError(t, err)

			found := false
			for _, mw := range tt.config.MiddlewareConfigs {
				if mw.Type == ratelimit.MiddlewareType {
					found = true
					break
				}
			}
			assert.Equal(t, tt.wantRateLimit, found)
		})
	}
}

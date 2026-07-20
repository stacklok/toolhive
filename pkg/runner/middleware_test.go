// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/auth/upstreamswap"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/oauthproto/tokenexchange"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	headerfwd "github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/transport/middleware/origin"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/webhook"
	"github.com/stacklok/toolhive/pkg/webhook/mutating"
	"github.com/stacklok/toolhive/pkg/webhook/validating"
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

func TestPrependOriginMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		config           *RunConfig
		wantPrepended    bool
		wantAllowedCount int
	}{
		{
			name:          "non-loopback bind without explicit allowlist skips middleware",
			config:        &RunConfig{Host: "0.0.0.0", Port: 8080},
			wantPrepended: false,
		},
		{
			name:          "zero port skips middleware",
			config:        &RunConfig{Host: "127.0.0.1", Port: 0},
			wantPrepended: false,
		},
		{
			name:             "loopback bind derives default allowlist and prepends",
			config:           &RunConfig{Host: "127.0.0.1", Port: 8080},
			wantPrepended:    true,
			wantAllowedCount: 3, // localhost + 127.0.0.1 + [::1]
		},
		{
			name:             "explicit allowlist on non-loopback bind prepends",
			config:           &RunConfig{Host: "0.0.0.0", Port: 8080, AllowedOrigins: []string{"https://app.example.com"}},
			wantPrepended:    true,
			wantAllowedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Seed with an existing entry so we can prove origin is prepended,
			// not appended — the security intent requires it to run first.
			initial := []types.MiddlewareConfig{{Type: auth.MiddlewareType}}
			got, err := prependOriginMiddleware(initial, tt.config)
			require.NoError(t, err)

			if !tt.wantPrepended {
				assert.Equal(t, initial, got, "middleware slice should be unchanged")
				return
			}

			require.Len(t, got, len(initial)+1)
			assert.Equal(t, origin.MiddlewareType, got[0].Type, "origin middleware must be first in the chain")
			assert.Equal(t, auth.MiddlewareType, got[1].Type, "pre-existing middleware must follow origin")

			var params origin.MiddlewareParams
			require.NoError(t, json.Unmarshal(got[0].Parameters, &params))
			assert.Len(t, params.AllowedOrigins, tt.wantAllowedCount)
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

// indexOfMiddleware returns the index of the first middleware of the given type
// in the chain, failing the test if it is absent.
func indexOfMiddleware(t *testing.T, mws []types.MiddlewareConfig, mwType string) int {
	t.Helper()
	for i, mw := range mws {
		if mw.Type == mwType {
			return i
		}
	}
	t.Fatalf("middleware type %q not found in chain", mwType)
	return -1
}

// TestPopulateMiddlewareConfigs_AdditionalMiddlewareConfigs verifies that
// configs injected via AdditionalMiddlewareConfigs survive
// PopulateMiddlewareConfigs (which previously overwrote the slice) and land in
// the backend-egress group: after auth and after header-forward, and
// immediately before recovery.
func TestPopulateMiddlewareConfigs_AdditionalMiddlewareConfigs(t *testing.T) {
	t.Parallel()

	mkInjected := func(t *testing.T, mwType string) types.MiddlewareConfig {
		t.Helper()
		mc, err := types.NewMiddlewareConfig(mwType, map[string]string{"marker": mwType})
		require.NoError(t, err)
		return *mc
	}

	t.Run("injected entry preserved, after auth and before recovery", func(t *testing.T) {
		t.Parallel()
		injected := mkInjected(t, obo.MiddlewareType)
		cfg := &RunConfig{
			AdditionalMiddlewareConfigs: []types.MiddlewareConfig{injected},
		}
		require.NoError(t, PopulateMiddlewareConfigs(cfg))

		mws := cfg.MiddlewareConfigs
		require.NotEmpty(t, mws)
		// body-limit is the outermost entry (slice index 0), auth runs next, and
		// recovery is always the last (innermost) entry.
		assert.Equal(t, bodylimit.MiddlewareType, mws[0].Type)
		assert.Equal(t, recovery.MiddlewareType, mws[len(mws)-1].Type)

		oboIdx := indexOfMiddleware(t, mws, obo.MiddlewareType)
		assert.Greater(t, oboIdx, indexOfMiddleware(t, mws, auth.MiddlewareType),
			"injected middleware must come after auth")
		assert.Equal(t, len(mws)-2, oboIdx,
			"injected middleware must be spliced immediately before recovery")
		// The opaque parameters are carried verbatim.
		assert.Equal(t, injected.Parameters, mws[oboIdx].Parameters)
	})

	t.Run("injected entry ordered after header-forward", func(t *testing.T) {
		t.Parallel()
		injected := mkInjected(t, obo.MiddlewareType)
		cfg := &RunConfig{
			RemoteURL: "https://example.com",
			HeaderForward: &HeaderForwardConfig{
				AddPlaintextHeaders: map[string]string{"X-Key": "val"},
			},
			AdditionalMiddlewareConfigs: []types.MiddlewareConfig{injected},
		}
		require.NoError(t, PopulateMiddlewareConfigs(cfg))

		mws := cfg.MiddlewareConfigs
		oboIdx := indexOfMiddleware(t, mws, obo.MiddlewareType)
		hfIdx := indexOfMiddleware(t, mws, headerfwd.HeaderForwardMiddlewareName)
		assert.Greater(t, oboIdx, hfIdx, "injected middleware must come after header-forward")
		assert.Equal(t, len(mws)-2, oboIdx, "injected middleware must be immediately before recovery")
		assert.Equal(t, recovery.MiddlewareType, mws[len(mws)-1].Type)
	})

	t.Run("multiple injected entries keep order before recovery", func(t *testing.T) {
		t.Parallel()
		cfg := &RunConfig{
			AdditionalMiddlewareConfigs: []types.MiddlewareConfig{
				mkInjected(t, "type-a"),
				mkInjected(t, "type-b"),
			},
		}
		require.NoError(t, PopulateMiddlewareConfigs(cfg))

		mws := cfg.MiddlewareConfigs
		n := len(mws)
		require.GreaterOrEqual(t, n, 4)
		assert.Equal(t, recovery.MiddlewareType, mws[n-1].Type)
		assert.Equal(t, "type-a", mws[n-3].Type)
		assert.Equal(t, "type-b", mws[n-2].Type)
	})

	t.Run("no injected configs leaves the chain unchanged", func(t *testing.T) {
		t.Parallel()
		base := &RunConfig{}
		require.NoError(t, PopulateMiddlewareConfigs(base))

		withEmpty := &RunConfig{AdditionalMiddlewareConfigs: nil}
		require.NoError(t, PopulateMiddlewareConfigs(withEmpty))

		require.Len(t, withEmpty.MiddlewareConfigs, len(base.MiddlewareConfigs))
		for i := range base.MiddlewareConfigs {
			assert.Equal(t, base.MiddlewareConfigs[i].Type, withEmpty.MiddlewareConfigs[i].Type)
		}
	})

	t.Run("re-running population keeps the injected entry exactly once", func(t *testing.T) {
		t.Parallel()
		cfg := &RunConfig{
			AdditionalMiddlewareConfigs: []types.MiddlewareConfig{mkInjected(t, obo.MiddlewareType)},
		}
		// PopulateMiddlewareConfigs overwrites MiddlewareConfigs (it rebuilds a fresh
		// local slice) rather than appending in place, so re-running it must not
		// accumulate duplicate injected entries. This invariant is what makes the
		// operator→proxyrunner path safe.
		require.NoError(t, PopulateMiddlewareConfigs(cfg))
		require.NoError(t, PopulateMiddlewareConfigs(cfg))

		count := 0
		for _, mw := range cfg.MiddlewareConfigs {
			if mw.Type == obo.MiddlewareType {
				count++
			}
		}
		assert.Equal(t, 1, count, "injected middleware must appear exactly once after re-population")
	})
}

// TestPopulateMiddlewareConfigs_AdditionalMiddlewareConfigs_RoundTrip exercises
// the full operator path (NewOperatorRunConfigBuilder + PopulateMiddlewareConfigs)
// and confirms an injected obo-typed config survives JSON/YAML serialization so
// the proxyrunner loads it and dispatches obo.MiddlewareType to a registered
// factory.
func TestPopulateMiddlewareConfigs_AdditionalMiddlewareConfigs_RoundTrip(t *testing.T) {
	t.Parallel()

	oboParams := map[string]string{"token_url": "https://login.example.com/token"}
	injected, err := types.NewMiddlewareConfig(obo.MiddlewareType, oboParams)
	require.NoError(t, err)

	cfg, err := NewOperatorRunConfigBuilder(
		t.Context(),
		nil,
		nil,
		nil,
		WithName("obo-test"),
		WithAdditionalMiddlewareConfigs(injected),
	)
	require.NoError(t, err)

	// The builder records the injected config but does not apply it; the
	// applied chain is populated separately by PopulateMiddlewareConfigs.
	require.Len(t, cfg.AdditionalMiddlewareConfigs, 1)
	require.Empty(t, cfg.MiddlewareConfigs)

	require.NoError(t, PopulateMiddlewareConfigs(cfg))

	findOBO := func(t *testing.T, c *RunConfig) types.MiddlewareConfig {
		t.Helper()
		return c.MiddlewareConfigs[indexOfMiddleware(t, c.MiddlewareConfigs, obo.MiddlewareType)]
	}

	original := findOBO(t, cfg)
	require.JSONEq(t, string(injected.Parameters), string(original.Parameters))

	t.Run("json", func(t *testing.T) {
		t.Parallel()
		// JSON is the proxyrunner's ConfigMap read path (WriteJSON/ReadJSON).
		var buf bytes.Buffer
		require.NoError(t, cfg.WriteJSON(&buf))
		got, err := ReadJSON(&buf)
		require.NoError(t, err)

		roundTripped := findOBO(t, got)
		assert.Equal(t, obo.MiddlewareType, roundTripped.Type)
		assert.JSONEq(t, string(original.Parameters), string(roundTripped.Parameters))
	})

	t.Run("yaml", func(t *testing.T) {
		t.Parallel()
		data, err := yaml.Marshal(cfg)
		require.NoError(t, err)

		var got RunConfig
		require.NoError(t, yaml.Unmarshal(data, &got))

		roundTripped := findOBO(t, &got)
		assert.Equal(t, obo.MiddlewareType, roundTripped.Type)
		assert.JSONEq(t, string(original.Parameters), string(roundTripped.Parameters))
	})

	// The proxyrunner dispatches by middleware type; the obo factory is wired.
	_, ok := GetSupportedMiddlewareFactories()[obo.MiddlewareType]
	assert.True(t, ok, "obo middleware type must dispatch to a registered factory")
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
		obo.MiddlewareType,
	} {
		_, ok := factories[key]
		assert.True(t, ok, "factory map should contain %q", key)
	}
}

// TestGetSupportedMiddlewareFactories_OBODispatchesToCurrentFactory locks the
// contract that obo.CreateMiddleware is a stable redirector: the literal map
// in GetSupportedMiddlewareFactories captures CreateMiddleware once, but each
// invocation reads obo's currentFactory under the package-level RWMutex, so
// registrations that happen AFTER the map is built still take effect. The
// register-then-call ordering exercises the production hot path; the
// call-after-register ordering exercises hot-reload / re-registration.
//
//nolint:paralleltest // Mutates package-level obo currentFactory; must not race other tests.
func TestGetSupportedMiddlewareFactories_OBODispatchesToCurrentFactory(t *testing.T) {
	// Build the factory map first so we capture the redirector before any
	// out-of-test registration happens.
	factories := GetSupportedMiddlewareFactories()
	factory, ok := factories[obo.MiddlewareType]
	require.True(t, ok, "obo factory should be present in the map")

	sentinel := []byte("custom-obo-factory")
	var observed []byte
	replacement := func(cfg *types.MiddlewareConfig, _ types.MiddlewareRunner) error {
		// Capture the marker the caller passes to prove which factory ran.
		observed = cfg.Parameters
		return nil
	}

	// Register AFTER the map was built. Because CreateMiddleware is a stable
	// redirector, dispatching through the map must still hit the replacement.
	obo.RegisterFactory(replacement)
	t.Cleanup(func() { obo.RegisterFactory(obo.DefaultFactory) })

	cfg := &types.MiddlewareConfig{Type: obo.MiddlewareType, Parameters: sentinel}
	require.NoError(t, factory(cfg, nil))
	assert.Equal(t, sentinel, observed,
		"obo.CreateMiddleware must redirect through the current factory, including after the runner map is built")
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
		wantType     string // expected middleware type when appended
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
			wantType:     upstreamswap.MiddlewareType,
		},
		{
			name: "DisableUpstreamTokenInjection adds strip-auth middleware instead",
			config: func() *RunConfig {
				cfg := createMinimalAuthServerConfig()
				cfg.DisableUpstreamTokenInjection = true
				return &RunConfig{EmbeddedAuthServerConfig: cfg}
			}(),
			wantAppended: true,
			wantType:     headerfwd.StripAuthMiddlewareName,
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
			wantType:     upstreamswap.MiddlewareType,
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
			wantType:     upstreamswap.MiddlewareType,
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
			assert.Equal(t, tt.wantType, added.Type)

			// For upstreamswap type, verify serialized params
			if tt.wantType == upstreamswap.MiddlewareType {
				var params upstreamswap.MiddlewareParams
				require.NoError(t, json.Unmarshal(added.Parameters, &params))

				if tt.config.UpstreamSwapConfig != nil {
					require.NotNil(t, params.Config)
					assert.Equal(t, tt.config.UpstreamSwapConfig.HeaderStrategy, params.Config.HeaderStrategy)
					assert.Equal(t, tt.config.UpstreamSwapConfig.CustomHeaderName, params.Config.CustomHeaderName)
				} else {
					require.NotNil(t, params.Config)
				}
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
		wantStripAuth      bool
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
			name: "DisableUpstreamTokenInjection adds strip-auth instead of upstream-swap",
			config: func() *RunConfig {
				cfg := createMinimalAuthServerConfig()
				cfg.DisableUpstreamTokenInjection = true
				return &RunConfig{EmbeddedAuthServerConfig: cfg}
			}(),
			wantUpstreamSwap: false,
			wantStripAuth:    true,
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

			var foundSwap bool
			var foundStrip bool
			var foundConfig *types.MiddlewareConfig
			for i, mw := range tt.config.MiddlewareConfigs {
				if mw.Type == upstreamswap.MiddlewareType {
					foundSwap = true
					foundConfig = &tt.config.MiddlewareConfigs[i]
				}
				if mw.Type == headerfwd.StripAuthMiddlewareName {
					foundStrip = true
				}
			}
			assert.Equal(t, tt.wantUpstreamSwap, foundSwap,
				"upstream-swap middleware presence mismatch")
			assert.Equal(t, tt.wantStripAuth, foundStrip,
				"strip-auth middleware presence mismatch")

			// Verify config values if we expect the middleware and have specific expectations
			if foundSwap && tt.wantHeaderStrategy != "" {
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

// TestPopulateMiddlewareConfigs_BodyLimitIsOutermost verifies that the body
// size limit middleware is always present and placed first in the config slice.
// The proxies apply middleware so the first config is the outermost wrapper (it
// executes first), which is required so oversized request bodies are rejected
// before the MCP parser or any proxy handler buffers them via io.ReadAll.
func TestPopulateMiddlewareConfigs_BodyLimitIsOutermost(t *testing.T) {
	t.Parallel()

	config := &RunConfig{}

	err := PopulateMiddlewareConfigs(config)
	require.NoError(t, err)

	require.NotEmpty(t, config.MiddlewareConfigs)
	assert.Equal(t, bodylimit.MiddlewareType, config.MiddlewareConfigs[0].Type,
		"body limit middleware must be first (outermost) in the chain")

	// It must come before auth, the MCP parser, and recovery.
	typeIndex := make(map[string]int, len(config.MiddlewareConfigs))
	for i, mw := range config.MiddlewareConfigs {
		typeIndex[mw.Type] = i
	}
	bodyLimitIdx := typeIndex[bodylimit.MiddlewareType]
	if authIdx, ok := typeIndex[auth.MiddlewareType]; ok {
		assert.Less(t, bodyLimitIdx, authIdx, "body limit must precede auth")
	}
	if parserIdx, ok := typeIndex[mcp.ParserMiddlewareType]; ok {
		assert.Less(t, bodyLimitIdx, parserIdx, "body limit must precede the MCP parser")
	}
}

// TestAddBodyLimitMiddleware verifies the shared helper that guarantees the
// body-size-limit middleware is the outermost (index 0) entry of the chain. This
// is the regression guard for the gap where pre-populated MiddlewareConfigs (e.g.
// via WithMiddlewareFromFlags on the `thv run`/management API path) bypassed
// PopulateMiddlewareConfigs and so never received a body cap.
func TestAddBodyLimitMiddleware(t *testing.T) {
	t.Parallel()

	// authConfig builds a non-body-limit middleware config to seed pre-populated chains.
	authConfig := func(t *testing.T) types.MiddlewareConfig {
		t.Helper()
		cfg, err := types.NewMiddlewareConfig(auth.MiddlewareType, auth.MiddlewareParams{})
		require.NoError(t, err)
		return *cfg
	}

	tests := []struct {
		name  string
		input func(t *testing.T) []types.MiddlewareConfig
		// assert receives the result and the (possibly nil) input for comparison.
		assert func(t *testing.T, input, result []types.MiddlewareConfig)
	}{
		{
			name:  "empty slice gets body limit at index 0",
			input: func(*testing.T) []types.MiddlewareConfig { return nil },
			assert: func(t *testing.T, _, result []types.MiddlewareConfig) {
				t.Helper()
				require.Len(t, result, 1)
				assert.Equal(t, bodylimit.MiddlewareType, result[0].Type)
			},
		},
		{
			name: "pre-populated slice without body limit gets it prepended (CLI/flags path)",
			input: func(t *testing.T) []types.MiddlewareConfig {
				t.Helper()
				return []types.MiddlewareConfig{authConfig(t)}
			},
			assert: func(t *testing.T, input, result []types.MiddlewareConfig) {
				t.Helper()
				require.Len(t, result, 2)
				assert.Equal(t, bodylimit.MiddlewareType, result[0].Type,
					"body limit must be prepended as the outermost entry")
				assert.Equal(t, input[0].Type, result[1].Type,
					"original entries must follow body limit, in order")
			},
		},
		{
			name: "slice already starting with body limit is unchanged (idempotent)",
			input: func(t *testing.T) []types.MiddlewareConfig {
				t.Helper()
				cfg, err := types.NewMiddlewareConfig(bodylimit.MiddlewareType, bodylimit.MiddlewareParams{
					MaxBytes: bodylimit.DefaultMaxRequestBodySize,
				})
				require.NoError(t, err)
				return []types.MiddlewareConfig{*cfg, authConfig(t)}
			},
			assert: func(t *testing.T, input, result []types.MiddlewareConfig) {
				t.Helper()
				require.Len(t, result, len(input), "no duplicate body limit should be added")
				assert.Equal(t, bodylimit.MiddlewareType, result[0].Type)
				// Exactly one body limit entry remains.
				count := 0
				for _, mw := range result {
					if mw.Type == bodylimit.MiddlewareType {
						count++
					}
				}
				assert.Equal(t, 1, count, "body limit must not be duplicated")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := tt.input(t)
			result, err := addBodyLimitMiddleware(input)
			require.NoError(t, err)
			tt.assert(t, input, result)
		})
	}
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
				RateLimitConfig: &v1beta1.RateLimitConfig{
					Shared: &v1beta1.RateLimitBucket{
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
				RateLimitConfig: &v1beta1.RateLimitConfig{
					Shared: &v1beta1.RateLimitBucket{
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
				RateLimitConfig: &v1beta1.RateLimitConfig{
					Shared: &v1beta1.RateLimitBucket{
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

func TestPopulateMiddlewareConfigs_FullCoverage(t *testing.T) {
	t.Parallel()

	config := NewRunConfig()
	config.Name = "test-server"
	config.Transport = types.TransportTypeStdio

	// Setup options to hit all branches
	config.MutatingWebhooks = []webhook.Config{{Name: "m-hook", URL: "http://example.com/m", Timeout: webhook.DefaultTimeout}}
	config.ValidatingWebhooks = []webhook.Config{{Name: "v-hook", URL: "http://example.com/v", Timeout: webhook.DefaultTimeout}}

	config.ToolsFilter = []string{"tool1"}
	config.ToolsOverride = map[string]ToolOverride{"tool1": {Name: "newtool1"}}

	config.TelemetryConfig = &telemetry.Config{}
	config.AuthzConfig = &authz.Config{}

	config.AuditConfig = &audit.Config{Component: "test-component"}

	err := PopulateMiddlewareConfigs(config)
	require.NoError(t, err)

	// Ensure they are populated
	typeIndex := make(map[string]bool)
	for _, mw := range config.MiddlewareConfigs {
		typeIndex[mw.Type] = true
	}

	assert.True(t, typeIndex[mutating.MiddlewareType])
	assert.True(t, typeIndex[validating.MiddlewareType])
	assert.True(t, typeIndex[mcp.ToolFilterMiddlewareType])
	assert.True(t, typeIndex[mcp.ToolCallFilterMiddlewareType])
	assert.True(t, typeIndex[telemetry.MiddlewareType])
	assert.True(t, typeIndex[authz.MiddlewareType])
	assert.True(t, typeIndex[audit.MiddlewareType])
}

// TestPopulateMiddlewareConfigs_AuditBeforeAuthz pins the ordering invariant
// that the audit middleware precedes authorization in the config slice.
// Earlier entries wrap later ones at request time, so audit must wrap authz
// for an authorization denial (403) to still produce an audit event with
// outcome "denied". It must in turn come after auth and the MCP parser, which
// provide the identity and parsed MCP data the audit event is built from.
func TestPopulateMiddlewareConfigs_AuditBeforeAuthz(t *testing.T) {
	t.Parallel()

	config := &RunConfig{
		AuthzConfig: &authz.Config{},
		AuditConfig: &audit.Config{Component: "test-component"},
	}

	require.NoError(t, PopulateMiddlewareConfigs(config))

	typeIndex := make(map[string]int, len(config.MiddlewareConfigs))
	for i, mw := range config.MiddlewareConfigs {
		typeIndex[mw.Type] = i
	}

	auditIdx, ok := typeIndex[audit.MiddlewareType]
	require.True(t, ok, "audit middleware must be present")
	authzIdx, ok := typeIndex[authz.MiddlewareType]
	require.True(t, ok, "authz middleware must be present")
	authIdx, ok := typeIndex[auth.MiddlewareType]
	require.True(t, ok, "auth middleware must be present")
	parserIdx, ok := typeIndex[mcp.ParserMiddlewareType]
	require.True(t, ok, "MCP parser middleware must be present")

	assert.Less(t, auditIdx, authzIdx,
		"audit must precede authz so authorization denials are audited")
	assert.Less(t, authIdx, auditIdx,
		"auth must precede audit so the identity is available to audit events")
	assert.Less(t, parserIdx, auditIdx,
		"MCP parser must precede audit so parsed MCP data is available to audit events")
}

// TestPopulateMiddlewareConfigs_StripAuthOrdering pins the ordering invariant
// for strip-auth: the auth middleware must precede it in the chain so the
// client JWT is fully validated (and the identity stored in the request
// context for authz/audit) before the Authorization header is removed.
func TestPopulateMiddlewareConfigs_StripAuthOrdering(t *testing.T) {
	t.Parallel()

	authServerCfg := createMinimalAuthServerConfig()
	authServerCfg.DisableUpstreamTokenInjection = true
	config := &RunConfig{EmbeddedAuthServerConfig: authServerCfg}

	require.NoError(t, PopulateMiddlewareConfigs(config))

	authIdx, stripIdx := -1, -1
	for i, mw := range config.MiddlewareConfigs {
		switch mw.Type {
		case auth.MiddlewareType:
			authIdx = i
		case headerfwd.StripAuthMiddlewareName:
			stripIdx = i
		}
	}
	require.GreaterOrEqual(t, authIdx, 0, "auth middleware must be present")
	require.GreaterOrEqual(t, stripIdx, 0, "strip-auth middleware must be present")
	assert.Less(t, authIdx, stripIdx,
		"auth must validate the client JWT before strip-auth removes the Authorization header")
}

// TestPopulateMiddlewareConfigs_StripAuthConflicts verifies that
// DisableUpstreamTokenInjection is rejected when combined with middlewares
// that would re-add credentials after the strip (token exchange, AWS STS),
// instead of silently defeating the flag at runtime.
func TestPopulateMiddlewareConfigs_StripAuthConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*RunConfig)
		wantErr string
	}{
		{
			name:    "token exchange combination is rejected",
			mutate:  func(c *RunConfig) { c.TokenExchangeConfig = &tokenexchange.Config{} },
			wantErr: "token exchange",
		},
		{
			name:    "AWS STS combination is rejected",
			mutate:  func(c *RunConfig) { c.AWSStsConfig = &awssts.Config{}; c.RemoteURL = "https://example.com" },
			wantErr: "AWS STS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authServerCfg := createMinimalAuthServerConfig()
			authServerCfg.DisableUpstreamTokenInjection = true
			config := &RunConfig{EmbeddedAuthServerConfig: authServerCfg}
			tt.mutate(config)

			err := PopulateMiddlewareConfigs(config)
			require.ErrorContains(t, err, "disableUpstreamTokenInjection cannot be combined")
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

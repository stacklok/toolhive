// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	headerfwd "github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

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

func TestGetSupportedMiddlewareFactories_IncludesHeaderForward(t *testing.T) {
	t.Parallel()

	factories := GetSupportedMiddlewareFactories()
	_, ok := factories[headerfwd.HeaderForwardMiddlewareName]
	assert.True(t, ok, "factory map should contain %q", headerfwd.HeaderForwardMiddlewareName)
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

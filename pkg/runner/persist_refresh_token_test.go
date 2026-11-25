// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
	"github.com/stacklok/toolhive/pkg/state"
	headerfwd "github.com/stacklok/toolhive/pkg/transport/middleware"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

//nolint:paralleltest // SaveState uses process-wide runtime and XDG state settings.
func TestRunner_PersistRefreshTokenKeepsResolvedSecretsOutOfState(t *testing.T) {
	setupPersistenceState(t)

	const (
		clientSecretReference = "client-secret-ref,target=oauth_secret"
		clientSecretValue     = "plaintext-client-secret"
		bearerTokenReference  = "bearer-token-ref,target=bearer_token"
		bearerTokenValue      = "plaintext-bearer-token"
		refreshTokenName      = "OAUTH_REFRESH_TOKEN_refresh-persistence"
	)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	secretManager := secretsmocks.NewMockProvider(ctrl)
	gomock.InOrder(
		secretManager.EXPECT().GetSecret(ctx, "client-secret-ref").Return(clientSecretValue, nil),
		secretManager.EXPECT().GetSecret(ctx, "bearer-token-ref").Return(bearerTokenValue, nil),
		secretManager.EXPECT().GetSecret(gomock.Any(), refreshTokenName).Return("", assert.AnError),
		secretManager.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true}),
		secretManager.EXPECT().SetSecret(ctx, refreshTokenName, "refresh-token-envelope").Return(nil),
	)

	remoteAuthConfig := &remote.Config{
		ClientSecret: clientSecretReference,
		BearerToken:  bearerTokenReference,
	}
	runConfig := NewRunConfig()
	runConfig.Name = "refresh-persistence"
	runConfig.BaseName = "refresh-persistence"
	runConfig.RemoteAuthConfig = remoteAuthConfig
	require.NoError(t, runConfig.SaveState(ctx))
	_, err := runConfig.WithSecrets(ctx, secretManager, secretManager)
	require.NoError(t, err)

	runner := &Runner{Config: runConfig}
	err = runner.persistRefreshToken(ctx, secretManager, "refresh-token-envelope", time.Now().Add(time.Hour))
	require.NoError(t, err)

	rawState := readPersistedState(t, runConfig.BaseName)
	assert.Contains(t, rawState, clientSecretReference)
	assert.Contains(t, rawState, bearerTokenReference)
	assert.NotContains(t, rawState, clientSecretValue)
	assert.NotContains(t, rawState, bearerTokenValue)

	tokenSource, err := remote.NewHandler(runConfig.RemoteAuthConfig).Authenticate(ctx, "https://example.com/mcp")
	require.NoError(t, err)
	require.NotNil(t, tokenSource)
	token, err := tokenSource.Token()
	require.NoError(t, err)
	assert.Equal(t, bearerTokenValue, token.AccessToken,
		"callback copy-back must preserve runtime-only credentials")
}

//nolint:paralleltest // SaveState uses process-wide runtime and XDG state settings.
func TestRunner_PersistRefreshToken_SaveStateFailurePreservesLiveConfig(t *testing.T) {
	setupPersistenceState(t)

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	secretManager := secretsmocks.NewMockProvider(ctrl)
	secretManager.EXPECT().GetSecret(gomock.Any(), "OAUTH_REFRESH_TOKEN_failed-refresh").Return("", assert.AnError)
	secretManager.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
	secretManager.EXPECT().SetSecret(ctx, "OAUTH_REFRESH_TOKEN_failed-refresh", "refresh-token-envelope").DoAndReturn(
		func(context.Context, string, string) error {
			blockPersistedStateSave(t, "failed-refresh")
			return nil
		})

	original := remote.Config{
		Issuer:                "https://issuer.example.com",
		TokenURL:              "https://issuer.example.com/token",
		CachedRefreshTokenRef: "OAUTH_REFRESH_TOKEN_previous",
		CachedTokenExpiry:     time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC),
	}
	remoteAuthConfig := original
	runConfig := NewRunConfig()
	runConfig.Name = "failed-refresh"
	runConfig.BaseName = "failed-refresh"
	runConfig.RemoteAuthConfig = &remoteAuthConfig
	require.NoError(t, runConfig.SaveState(ctx))
	runner := &Runner{Config: runConfig}

	err := runner.persistRefreshToken(ctx, secretManager, "refresh-token-envelope", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save config with token reference")
	assert.Same(t, &remoteAuthConfig, runner.Config.RemoteAuthConfig)
	assert.Equal(t, original, *runner.Config.RemoteAuthConfig)
}

// setupPersistenceState isolates SaveState's process-wide runtime and XDG state
// settings; callers must stay sequential. It restores xdg's cached paths after
// t.Setenv restores the environment.
func setupPersistenceState(t *testing.T) {
	t.Helper()
	t.Cleanup(xdg.Reload)
	t.Setenv("TOOLHIVE_RUNTIME", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()
}

func blockPersistedStateSave(t *testing.T, name string) {
	t.Helper()
	// A directory in place of the state file makes the callback's later SaveState fail.
	require.NoError(t, state.DeleteSavedRunConfig(context.Background(), name))
	require.NoError(t, os.Mkdir(filepath.Join(xdg.StateHome, state.DefaultAppName, state.RunConfigsDir, name+state.FileExtension), 0700))
}

func readPersistedState(t *testing.T, name string) string {
	t.Helper()
	reader, err := state.LoadRunConfigJSON(context.Background(), name)
	require.NoError(t, err)
	raw, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return string(raw)
}

func loadPersistedRunConfig(t *testing.T, name string) (*RunConfig, string) {
	t.Helper()
	raw := readPersistedState(t, name)
	var config RunConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &config))
	return &config, raw
}

func writePersistedState(t *testing.T, name, raw string) {
	t.Helper()
	store, err := state.NewRunConfigStore(state.DefaultAppName)
	require.NoError(t, err)
	writer, err := store.GetWriter(context.Background(), name)
	require.NoError(t, err)
	_, err = io.WriteString(writer, raw)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
}

func persistTestAuth(ctx context.Context, r *Runner, provider secrets.Provider, callback string) error {
	expiry := time.Date(2027, time.January, 2, 3, 4, 5, 0, time.UTC)
	if callback == "refresh" {
		return r.persistRefreshToken(ctx, provider, "synthetic-refresh-token", expiry)
	}
	return r.persistClientCredentials(ctx, provider, "new-client-id", "synthetic-client-secret", expiry,
		"synthetic-registration-token", "https://issuer.example.com/register/client", "client_secret_basic", 9876)
}

//nolint:paralleltest // Uses process-wide runtime and XDG settings.
func TestRunner_PersistAuthSavesStoredConfig(t *testing.T) {
	setupPersistenceState(t)
	ctx := context.Background()
	expiry := time.Date(2027, time.January, 2, 3, 4, 5, 0, time.UTC)
	// buildLive mirrors the middleware assembly order in Runner.Run; keep them in sync.
	buildLive := func(t *testing.T, name string, provider secrets.Provider) *RunConfig {
		t.Helper()
		live, err := LoadState(ctx, name)
		require.NoError(t, err)
		_, err = live.WithSecrets(ctx, provider, provider)
		require.NoError(t, err)
		if len(live.MiddlewareConfigs) == 0 {
			require.NoError(t, PopulateMiddlewareConfigs(live))
		} else {
			live.MiddlewareConfigs, err = addHeaderForwardMiddleware(live.MiddlewareConfigs, live)
			require.NoError(t, err)
		}
		live.MiddlewareConfigs, err = prependOriginMiddleware(live.MiddlewareConfigs, live)
		require.NoError(t, err)
		live.MiddlewareConfigs, err = addBodyLimitMiddleware(live.MiddlewareConfigs, live.MaxRequestBodySize)
		require.NoError(t, err)
		return live
	}

	for _, callback := range []string{"refresh", "client"} {
		for _, start := range []string{"fresh", "prebuilt", "leaked"} {
			prebuilt := start == "prebuilt"
			t.Run(fmt.Sprintf("%s/%s", callback, start), func(t *testing.T) {
				provider := secretsmocks.NewMockProvider(gomock.NewController(t))
				provider.EXPECT().GetSecret(ctx, "env-secret").Return("synthetic-resolved-env", nil).AnyTimes()
				provider.EXPECT().GetSecret(ctx, "header-secret").Return("synthetic-resolved-header", nil).AnyTimes()
				provider.EXPECT().GetSecret(gomock.Any(), gomock.Any()).Return("", assert.AnError).AnyTimes()
				provider.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true}).AnyTimes()
				provider.EXPECT().SetSecret(ctx, gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

				stored := NewRunConfig()
				stored.Name = fmt.Sprintf("%s-%s", callback, start)
				stored.BaseName = stored.Name
				stored.RemoteURL = "https://example.com/mcp"
				stored.AllowedOrigins = []string{"https://client.example.com"}
				stored.RemoteAuthConfig = &remote.Config{Issuer: "https://issuer.example.com", CachedCIMDClientID: "https://client.example.com"}
				stored.Secrets = []string{"env-secret,target=SECRET_ENV"}
				stored.EnvVars = map[string]string{"PUBLIC_ENV": "public"}
				stored.HeaderForward = &HeaderForwardConfig{
					AddHeadersFromSecret: map[string]string{"X-Api-Key": "header-secret"},
					AddPlaintextHeaders:  map[string]string{"X-Public": "public"},
				}
				if prebuilt {
					stored.MiddlewareConfigs = []types.MiddlewareConfig{
						{Type: "auth", Parameters: json.RawMessage(`{"oidc_config":null}`)},
						{Type: headerfwd.StripAuthMiddlewareName, Parameters: json.RawMessage(`{}`)},
					}
				}
				if start == "leaked" {
					// State written by older versions carried resolved secret values.
					writePersistedState(t, stored.BaseName, fmt.Sprintf(`{"name":%q,"base_name":%q,`+
						`"remote_url":"https://example.com/mcp","remote_auth_config":{"issuer":"https://issuer.example.com"},`+
						`"secrets":["env-secret,target=SECRET_ENV"],`+
						`"env_vars":{"SECRET_ENV":"leaked-env","PUBLIC_ENV":"public"},`+
						`"header_forward":{"add_headers_from_secret":{"X-Api-Key":"header-secret"}},`+
						`"middleware_configs":[{"type":"header-forward","parameters":`+
						`{"add_headers":{"X-Api-Key":"leaked-header","X-Public":"public"}}}]}`, stored.Name, stored.Name))
					live := buildLive(t, stored.BaseName, provider)
					require.NoError(t, persistTestAuth(ctx, &Runner{Config: live}, provider, callback))
					raw := readPersistedState(t, stored.BaseName)
					assert.NotContains(t, raw, "leaked-env")
					assert.NotContains(t, raw, "leaked-header")
					assert.Contains(t, raw, "PUBLIC_ENV")
					assert.Contains(t, raw, "X-Public")
					return
				}
				require.NoError(t, stored.SaveState(ctx))
				baseline, _ := loadPersistedRunConfig(t, stored.BaseName)

				live := buildLive(t, stored.BaseName, provider)
				firstChain := live.MiddlewareConfigs
				// An edit completed before the callback's load must survive.
				stored.Group = "edited-group"
				require.NoError(t, stored.SaveState(ctx))
				live.RemoteAuthConfig.SetResolvedClientSecret("runtime-only-client")
				live.RemoteAuthConfig.SetResolvedBearerToken("runtime-only-bearer")
				live.EnvVars["RUNTIME_ONLY"] = "runtime-only-env"
				authPointer := live.RemoteAuthConfig
				expectedAuth := *authPointer

				require.NoError(t, persistTestAuth(ctx, &Runner{Config: live}, provider, callback))
				assert.Same(t, authPointer, live.RemoteAuthConfig)
				if callback == "refresh" {
					expectedAuth.CachedRefreshTokenRef = authPointer.CachedRefreshTokenRef
					expectedAuth.CachedTokenExpiry = expiry
					assert.NotEmpty(t, expectedAuth.CachedRefreshTokenRef)
				} else {
					expectedAuth.CachedClientID = "new-client-id"
					expectedAuth.CachedClientSecretRef = authPointer.CachedClientSecretRef
					expectedAuth.CachedRegTokenRef = authPointer.CachedRegTokenRef
					expectedAuth.CachedSecretExpiry = expiry
					expectedAuth.CachedRegClientURI = "https://issuer.example.com/register/client"
					expectedAuth.CachedTokenEndpointAuthMethod = "client_secret_basic"
					expectedAuth.CachedDCRCallbackPort = 9876
					assert.NotEmpty(t, expectedAuth.CachedClientSecretRef)
					assert.NotEmpty(t, expectedAuth.CachedRegTokenRef)
				}
				assert.Equal(t, expectedAuth, *authPointer)

				persisted, raw := loadPersistedRunConfig(t, stored.BaseName)
				for _, value := range []string{"synthetic-resolved-env", "synthetic-resolved-header", "runtime-only-env",
					"synthetic-refresh-token", "synthetic-client-secret", "synthetic-registration-token",
					"runtime-only-client", "runtime-only-bearer"} {
					assert.NotContains(t, raw, value)
				}
				assert.Equal(t, baseline.EnvVars, persisted.EnvVars)
				assert.Equal(t, baseline.HeaderForward, persisted.HeaderForward)
				assert.Equal(t, baseline.MiddlewareConfigs, persisted.MiddlewareConfigs)
				assert.Equal(t, "edited-group", persisted.Group)
				expectedAuth.ClearResolvedSecrets()
				assert.Equal(t, expectedAuth, *persisted.RemoteAuthConfig)

				assert.Equal(t, firstChain, buildLive(t, stored.BaseName, provider).MiddlewareConfigs,
					"restart must rebuild the same chain from the persisted config")
			})
		}
	}
}

//nolint:paralleltest // Uses process-wide runtime and XDG settings.
func TestLoadAndScrubRunConfig(t *testing.T) {
	setupPersistenceState(t)
	ctx := context.Background()
	const authParams, stripParams = `{"oidc_config":{"issuer":"keep"}}`, `{}`
	tests := []struct {
		name       string
		entry      string
		wantParams string // empty means the entry must have no parameters
	}{
		{
			name:       "secret header removed in another case, others kept",
			entry:      `{"add_headers":{"x-aPI-kEy":"legacy-value","X-Public":"keep","X-Other":"other"}}`,
			wantParams: `{"add_headers":{"X-Public":"keep","X-Other":"other"}}`,
		},
		{
			name:       "case-variant key and duplicate members cleaned",
			entry:      `{"ADD_HEADERS":{"X-Api-Key":"legacy-one"},"add_headers":{"X-API-KEY":"legacy-two","X-Public":"keep"}}`,
			wantParams: `{"add_headers":{"X-Public":"keep"}}`,
		},
		{name: "empty parameters skipped", entry: ``},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("scrub-%d", i)
			entry := `{"type":"header-forward"}`
			if tc.entry != "" {
				entry = `{"type":"header-forward","parameters":` + tc.entry + `}`
			}
			raw := fmt.Sprintf(`{"name":%q,"base_name":%q,"secrets":["env-ref,target=SECRET_ENV"],`+
				`"env_vars":{"SECRET_ENV":"legacy-env","PUBLIC_ENV":"keep"},`+
				`"header_forward":{"add_headers_from_secret":{"X-Api-Key":"header-ref"}},`+
				`"middleware_configs":[{"type":"auth","parameters":%s},%s,{"type":"strip-auth","parameters":%s}]}`,
				name, name, authParams, entry, stripParams)
			writePersistedState(t, name, raw)
			live := NewRunConfig()
			live.Name, live.BaseName = name, name
			live.EnvVars = map[string]string{"SECRET_ENV": "live"}

			scrubbed, err := (&Runner{Config: live}).loadAndScrubRunConfig(ctx)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"PUBLIC_ENV": "keep"}, scrubbed.EnvVars)
			require.Len(t, scrubbed.MiddlewareConfigs, 3)
			assert.Equal(t, "auth", scrubbed.MiddlewareConfigs[0].Type)
			assert.JSONEq(t, authParams, string(scrubbed.MiddlewareConfigs[0].Parameters))
			assert.Equal(t, headerfwd.HeaderForwardMiddlewareName, scrubbed.MiddlewareConfigs[1].Type)
			if tc.wantParams == "" {
				assert.Empty(t, scrubbed.MiddlewareConfigs[1].Parameters)
			} else {
				assert.JSONEq(t, tc.wantParams, string(scrubbed.MiddlewareConfigs[1].Parameters))
			}
			assert.Equal(t, "strip-auth", scrubbed.MiddlewareConfigs[2].Type)
			assert.JSONEq(t, stripParams, string(scrubbed.MiddlewareConfigs[2].Parameters))
			assert.Equal(t, map[string]string{"SECRET_ENV": "live"}, live.EnvVars)
			assert.Equal(t, raw, readPersistedState(t, name), "helper must not write state")
		})
	}
}

//nolint:paralleltest // Uses process-wide runtime and XDG settings.
func TestRunner_PersistAuthFailsClosed(t *testing.T) {
	setupPersistenceState(t)
	ctx := context.Background()
	tests := []struct {
		name         string
		raw          string // empty means no stored state
		wantError    string
		providerFail bool
	}{
		{name: "missing state", wantError: "failed to load stored run configuration"},
		{name: "corrupt JSON", raw: `{"synthetic-sensitive-input":`, wantError: "invalid stored run configuration"},
		{name: "null file", raw: `null`, wantError: "stored run configuration is empty"},
		{name: "invalid secret reference", raw: `{"secrets":["synthetic-sensitive-input"]}`,
			wantError: "invalid stored secret reference"},
		{name: "malformed header parameters",
			raw: `{"header_forward":{"add_headers_from_secret":{"X-Key":"ref"}},"middleware_configs":` +
				`[{"type":"header-forward","parameters":{"add_headers":{"synthetic-sensitive-input":42}}}]}`,
			wantError: "invalid stored header forwarding parameters"},
		{name: "secrets provider failure", raw: `{}`, providerFail: true},
	}
	for i, tc := range tests {
		for _, callback := range []string{"refresh", "client"} {
			t.Run(tc.name+"/"+callback, func(t *testing.T) {
				live := NewRunConfig()
				live.Name = fmt.Sprintf("closed-%d-%s", i, callback)
				live.BaseName = live.Name
				live.RemoteAuthConfig = &remote.Config{CachedClientID: "unchanged"}
				authPointer, original := live.RemoteAuthConfig, *live.RemoteAuthConfig
				if tc.raw != "" {
					writePersistedState(t, live.BaseName, tc.raw)
				}
				// Strict mock: any provider call before state validation fails the test.
				provider := secretsmocks.NewMockProvider(gomock.NewController(t))
				if tc.providerFail {
					provider.EXPECT().GetSecret(gomock.Any(), gomock.Any()).Return("", assert.AnError).AnyTimes()
					provider.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true}).AnyTimes()
					provider.EXPECT().SetSecret(ctx, gomock.Any(), gomock.Any()).Return(assert.AnError)
				}

				err := persistTestAuth(ctx, &Runner{Config: live}, provider, callback)
				require.Error(t, err)
				if tc.providerFail {
					assert.ErrorIs(t, err, assert.AnError)
				} else {
					assert.ErrorContains(t, err, tc.wantError)
				}
				assert.NotContains(t, fmt.Sprintf("%+v", err), "synthetic-sensitive-input")
				assert.Same(t, authPointer, live.RemoteAuthConfig)
				assert.Equal(t, original, *live.RemoteAuthConfig)

				if tc.raw == "" {
					store, storeErr := state.NewRunConfigStore(state.DefaultAppName)
					require.NoError(t, storeErr)
					exists, existsErr := store.Exists(ctx, live.BaseName)
					require.NoError(t, existsErr)
					assert.False(t, exists, "no state may be recreated")
				} else {
					assert.Equal(t, tc.raw, readPersistedState(t, live.BaseName))
				}
			})
		}
	}
}

//nolint:paralleltest // Uses process-wide runtime and XDG settings.
func TestRunner_PersistClientCredentialsWithNilAuth(t *testing.T) {
	setupPersistenceState(t)
	ctx := context.Background()
	live := NewRunConfig()
	live.Name, live.BaseName = "nil-auth", "nil-auth"
	require.NoError(t, live.SaveState(ctx))
	provider := secretsmocks.NewMockProvider(gomock.NewController(t))
	require.NoError(t, (&Runner{Config: live}).persistClientCredentials(ctx, provider, "public-client", "", time.Time{}, "", "", "none", 1234))
	require.NotNil(t, live.RemoteAuthConfig)
	persisted, _ := loadPersistedRunConfig(t, live.BaseName)
	assert.Equal(t, live.RemoteAuthConfig, persisted.RemoteAuthConfig)
	assert.Equal(t, "public-client", persisted.RemoteAuthConfig.CachedClientID)
}

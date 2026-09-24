// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"io"
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
)

//nolint:paralleltest // SaveState uses process-wide runtime and XDG state settings.
func TestRunner_PersistRefreshTokenKeepsResolvedSecretsOutOfState(t *testing.T) {
	t.Cleanup(xdg.Reload)
	t.Setenv("TOOLHIVE_RUNTIME", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

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
	_, err := runConfig.WithSecrets(ctx, secretManager, secretManager)
	require.NoError(t, err)

	runner := &Runner{Config: runConfig}
	err = runner.persistRefreshToken(ctx, secretManager, "refresh-token-envelope", time.Now().Add(time.Hour))
	require.NoError(t, err)

	reader, err := state.LoadRunConfigJSON(ctx, runConfig.BaseName)
	require.NoError(t, err)
	rawState, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Contains(t, string(rawState), clientSecretReference)
	assert.Contains(t, string(rawState), bearerTokenReference)
	assert.NotContains(t, string(rawState), clientSecretValue)
	assert.NotContains(t, string(rawState), bearerTokenValue)

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
	t.Cleanup(xdg.Reload)
	t.Setenv("TOOLHIVE_RUNTIME", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

	ctx := context.Background()
	ctrl := gomock.NewController(t)
	secretManager := secretsmocks.NewMockProvider(ctrl)
	secretManager.EXPECT().GetSecret(gomock.Any(), "OAUTH_REFRESH_TOKEN_failed-refresh").Return("", assert.AnError)
	secretManager.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
	secretManager.EXPECT().SetSecret(ctx, "OAUTH_REFRESH_TOKEN_failed-refresh", "refresh-token-envelope").Return(nil)

	original := remote.Config{
		Issuer:                "https://issuer.example.com",
		TokenURL:              "https://issuer.example.com/token",
		CachedRefreshTokenRef: "OAUTH_REFRESH_TOKEN_previous",
		CachedTokenExpiry:     time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC),
	}
	remoteAuthConfig := original
	runConfig := NewRunConfig()
	runConfig.Name = "failed-refresh"
	runConfig.BaseName = "../invalid"
	runConfig.RemoteAuthConfig = &remoteAuthConfig
	runner := &Runner{Config: runConfig}

	err := runner.persistRefreshToken(ctx, secretManager, "refresh-token-envelope", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save config with token reference")
	assert.Same(t, &remoteAuthConfig, runner.Config.RemoteAuthConfig)
	assert.Equal(t, original, *runner.Config.RemoteAuthConfig)
}

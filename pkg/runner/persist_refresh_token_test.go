// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

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

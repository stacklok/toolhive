// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/secrets"
)

func TestResolveSecretFromFile(t *testing.T) {
	t.Parallel()

	secretPath := filepath.Join(t.TempDir(), "hmac")
	require.NoError(t, os.WriteFile(secretPath, []byte("top-secret"), 0o600))

	value, err := ResolveSecret(t.Context(), secretPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("top-secret"), value)
}

func TestResolveSecret(t *testing.T) {
	t.Run("empty ref short-circuits before provider setup", func(t *testing.T) {
		t.Setenv(secrets.ProviderEnvVar, "unknown")

		value, err := ResolveSecret(t.Context(), "")
		require.NoError(t, err)
		assert.Nil(t, value)
	})

	t.Run("env var provider resolves raw secret name", func(t *testing.T) {
		t.Setenv(secrets.ProviderEnvVar, string(secrets.EnvironmentType))
		t.Setenv(secrets.EnvVarPrefix+"webhook_hmac", "top-secret")

		value, err := ResolveSecret(t.Context(), "webhook_hmac")
		require.NoError(t, err)
		assert.Equal(t, []byte("top-secret"), value)
	})

	t.Run("env var provider resolves CLI-style secret reference by name", func(t *testing.T) {
		t.Setenv(secrets.ProviderEnvVar, string(secrets.EnvironmentType))
		t.Setenv(secrets.EnvVarPrefix+"webhook_hmac", "top-secret")

		value, err := ResolveSecret(t.Context(), "webhook_hmac,target=ignored")
		require.NoError(t, err)
		assert.Equal(t, []byte("top-secret"), value)
	})

	t.Run("secrets setup incomplete returns sentinel error", func(t *testing.T) {
		t.Setenv(secrets.ProviderEnvVar, "")
		config.SetSingletonConfig(&config.Config{})
		t.Cleanup(config.ResetSingleton)

		value, err := ResolveSecret(t.Context(), "webhook_hmac")
		require.Error(t, err)
		assert.Nil(t, value)
		assert.True(t, errors.Is(err, secrets.ErrSecretsNotSetup))
	})

	t.Run("missing secret returns wrapped error with secret name", func(t *testing.T) {
		t.Setenv(secrets.ProviderEnvVar, string(secrets.EnvironmentType))

		value, err := ResolveSecret(t.Context(), "missing_hmac,target=ignored")
		require.Error(t, err)
		assert.Nil(t, value)
		assert.Contains(t, err.Error(), `failed to resolve webhook secret "missing_hmac"`)
		assert.True(t, strings.Contains(err.Error(), "secret not found"))
	})
}

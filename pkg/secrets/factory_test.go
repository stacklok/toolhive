// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package secrets_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets"
)

const (
	testSecretValue = "fallback_value"
)

func TestCreateSecretProvider(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("environment provider", func(t *testing.T) { //nolint:paralleltest
		provider, err := secrets.CreateSecretProvider(secrets.EnvironmentType)
		require.NoError(t, err)
		require.NotNil(t, provider)

		// Verify it's an environment provider by checking capabilities
		caps := provider.Capabilities()
		assert.True(t, caps.CanRead)
		assert.False(t, caps.CanWrite)
		assert.False(t, caps.CanDelete)
		assert.False(t, caps.CanList)
		assert.False(t, caps.CanCleanup)

		// Test basic functionality
		_, err = provider.GetSecret(ctx, "nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "secret not found")
	})

	t.Run("unknown provider type", func(t *testing.T) { //nolint:paralleltest
		provider, err := secrets.CreateSecretProvider(secrets.ProviderType("unknown"))
		assert.Error(t, err)
		assert.Nil(t, provider)
		assert.Equal(t, secrets.ErrUnknownManagerType, err)
	})
}

func TestCreateSecretProviderWithPassword(t *testing.T) { //nolint:paralleltest
	t.Run("environment provider ignores password", func(t *testing.T) { //nolint:paralleltest
		provider, err := secrets.CreateSecretProviderWithPassword(secrets.EnvironmentType, "ignored_password")
		require.NoError(t, err)
		require.NotNil(t, provider)

		// Verify it's still an environment provider
		caps := provider.Capabilities()
		assert.True(t, caps.CanRead)
		assert.False(t, caps.CanWrite)
	})
}

func TestValidateProvider(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("environment provider validation", func(t *testing.T) { //nolint:paralleltest
		result := secrets.ValidateProvider(ctx, secrets.EnvironmentType)
		require.NotNil(t, result)
		assert.Equal(t, secrets.EnvironmentType, result.ProviderType)
		assert.True(t, result.Success)
		assert.Contains(t, result.Message, "Environment provider validation successful")
		assert.NoError(t, result.Error)
	})

	t.Run("unknown provider validation", func(t *testing.T) { //nolint:paralleltest
		result := secrets.ValidateProvider(ctx, secrets.ProviderType("unknown"))
		require.NotNil(t, result)
		assert.Equal(t, secrets.ProviderType("unknown"), result.ProviderType)
		assert.False(t, result.Success)
		assert.Contains(t, result.Message, "Failed to initialize unknown provider")
		assert.Error(t, result.Error)
	})
}

func TestValidateEnvironmentProvider(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("successful validation", func(t *testing.T) { //nolint:paralleltest
		provider := secrets.NewEnvironmentProvider()
		result := &secrets.SetupResult{
			ProviderType: secrets.EnvironmentType,
			Success:      false,
		}

		result = secrets.ValidateEnvironmentProvider(ctx, provider, result)
		assert.True(t, result.Success)
		assert.Contains(t, result.Message, "Environment provider validation successful")
		assert.NoError(t, result.Error)
	})
}

func TestProviderTypes(t *testing.T) { //nolint:paralleltest
	t.Run("all provider types are valid strings", func(t *testing.T) { //nolint:paralleltest
		assert.Equal(t, "encrypted", string(secrets.EncryptedType))
		assert.Equal(t, "1password", string(secrets.OnePasswordType))
		assert.Equal(t, "environment", string(secrets.EnvironmentType))
	})
}

func TestEnvVarPrefix(t *testing.T) { //nolint:paralleltest
	t.Run("correct prefix constant", func(t *testing.T) { //nolint:paralleltest
		assert.Equal(t, "TOOLHIVE_SECRET_", secrets.EnvVarPrefix)
	})
}

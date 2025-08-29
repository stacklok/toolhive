package secrets_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

const (
	testSecretName = "test_secret"
)

func TestFallbackProvider_IntegrationTests(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("primary provider success", func(t *testing.T) { //nolint:paralleltest
		// Create mock primary provider
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().GetSecret(ctx, "test_secret").Return("primary_value", nil)

		// Create fallback provider
		fallback := secrets.NewFallbackProvider(mockPrimary)

		// Test - should get value from primary provider
		result, err := fallback.GetSecret(ctx, "test_secret")
		assert.NoError(t, err)
		assert.Equal(t, "primary_value", result)
	})

	t.Run("primary provider not found, fallback success", func(t *testing.T) { //nolint:paralleltest
		// Set up environment variable for fallback
		secretName := "fallback_secret"
		secretValue := testSecretValue
		envVar := secrets.EnvVarPrefix + secretName

		err := os.Setenv(envVar, secretValue)
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		// Create mock primary provider that returns "not found" error
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().GetSecret(ctx, secretName).Return("", errors.New("secret not found: "+secretName))

		// Create fallback provider
		fallback := secrets.NewFallbackProvider(mockPrimary)

		// Test - should get value from environment fallback
		result, err := fallback.GetSecret(ctx, secretName)
		assert.NoError(t, err)
		assert.Equal(t, secretValue, result)
	})

	t.Run("mixed primary and fallback secrets", func(t *testing.T) { //nolint:paralleltest
		// Set up environment variables
		err := os.Setenv(secrets.EnvVarPrefix+"env_only", "env_value")
		require.NoError(t, err)
		defer os.Unsetenv(secrets.EnvVarPrefix + "env_only")

		// Create mock primary provider
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)

		// Primary has this secret
		mockPrimary.EXPECT().GetSecret(ctx, "primary_secret").Return("primary_value", nil)

		// Primary doesn't have this secret (fallback will be used)
		mockPrimary.EXPECT().GetSecret(ctx, "env_only").Return("", errors.New("secret not found: env_only"))

		fallback := secrets.NewFallbackProvider(mockPrimary)

		// Test primary secret
		result, err := fallback.GetSecret(ctx, "primary_secret")
		assert.NoError(t, err)
		assert.Equal(t, "primary_value", result)

		// Test fallback secret
		result, err = fallback.GetSecret(ctx, "env_only")
		assert.NoError(t, err)
		assert.Equal(t, "env_value", result)
	})
}

func TestEnvironmentProvider_IntegrationTests(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()
	ctx := context.Background()

	t.Run("successful retrieval", func(t *testing.T) { //nolint:paralleltest
		// Set up environment variable
		secretName := testSecretName
		secretValue := "test_value"
		envVar := secrets.EnvVarPrefix + secretName

		err := os.Setenv(envVar, secretValue)
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		// Test retrieval
		result, err := provider.GetSecret(ctx, secretName)
		assert.NoError(t, err)
		assert.Equal(t, secretValue, result)
	})

	t.Run("multiple secrets", func(t *testing.T) { //nolint:paralleltest
		// Set up multiple environment variables
		testSecrets := map[string]string{
			"api_key":      "key123",
			"database_url": "postgres://localhost/test",
			"token":        "abc-def-ghi",
		}

		// Set environment variables
		for name, value := range testSecrets {
			envVar := secrets.EnvVarPrefix + name
			err := os.Setenv(envVar, value)
			require.NoError(t, err)
			defer os.Unsetenv(envVar)
		}

		// Test retrieval of all secrets
		for name, expectedValue := range testSecrets {
			result, err := provider.GetSecret(ctx, name)
			assert.NoError(t, err)
			assert.Equal(t, expectedValue, result)
		}
	})

	t.Run("special characters in secret names", func(t *testing.T) { //nolint:paralleltest
		testCases := []struct {
			name  string
			value string
		}{
			{"api-key", "value1"},
			{"API_KEY", "value2"},
			{"secret.name", "value3"},
			{"secret_123", "value4"},
		}

		for _, tc := range testCases {
			envVar := secrets.EnvVarPrefix + tc.name
			err := os.Setenv(envVar, tc.value)
			require.NoError(t, err)
			defer os.Unsetenv(envVar)

			result, err := provider.GetSecret(ctx, tc.name)
			assert.NoError(t, err)
			assert.Equal(t, tc.value, result)
		}
	})
}

func TestFactoryIntegration(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("fallback disabled creates direct provider", func(t *testing.T) { //nolint:paralleltest
		// Disable fallback
		err := os.Setenv("TOOLHIVE_DISABLE_ENV_FALLBACK", "true")
		require.NoError(t, err)
		defer os.Unsetenv("TOOLHIVE_DISABLE_ENV_FALLBACK")

		// Set up environment variable that should NOT be accessible
		secretName := "disabled_fallback_test"
		secretValue := "should_not_be_accessible"
		envVar := secrets.EnvVarPrefix + secretName

		err = os.Setenv(envVar, secretValue)
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		// Create none provider (should not have fallback)
		provider, err := secrets.CreateSecretProvider(secrets.NoneType)
		require.NoError(t, err)

		// Should not be able to access environment variable
		result, err := provider.GetSecret(ctx, secretName)
		assert.Error(t, err)
		assert.Empty(t, result)
		assert.Contains(t, err.Error(), "none provider doesn't store secrets")
	})

	t.Run("fallback enabled allows environment access", func(t *testing.T) { //nolint:paralleltest
		// Ensure fallback is enabled
		os.Unsetenv("TOOLHIVE_DISABLE_ENV_FALLBACK")

		// Set up environment variable
		secretName := "enabled_fallback_test"
		secretValue := "should_be_accessible"
		envVar := secrets.EnvVarPrefix + secretName

		err := os.Setenv(envVar, secretValue)
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		// Create none provider (should have fallback)
		provider, err := secrets.CreateSecretProvider(secrets.NoneType)
		require.NoError(t, err)

		// Should be able to access environment variable via fallback
		result, err := provider.GetSecret(ctx, secretName)
		assert.NoError(t, err)
		assert.Equal(t, secretValue, result)
	})
}

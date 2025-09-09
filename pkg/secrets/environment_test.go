package secrets_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/secrets"
)

func TestEnvironmentProvider_GetSecret(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()
	ctx := context.Background()

	t.Run("successful retrieval", func(t *testing.T) { //nolint:paralleltest
		// Set up environment variable
		secretName := "test_secret"
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

	t.Run("secret not found", func(t *testing.T) { //nolint:paralleltest
		secretName := "nonexistent_secret"

		// Ensure the environment variable doesn't exist
		envVar := secrets.EnvVarPrefix + secretName
		os.Unsetenv(envVar)

		// Test retrieval
		result, err := provider.GetSecret(ctx, secretName)
		assert.Error(t, err)
		assert.Empty(t, result)
		assert.Contains(t, err.Error(), "secret not found")
	})

	t.Run("empty secret name", func(t *testing.T) { //nolint:paralleltest
		result, err := provider.GetSecret(ctx, "")
		assert.Error(t, err)
		assert.Empty(t, result)
		assert.Contains(t, err.Error(), "secret name cannot be empty")
	})

	t.Run("empty environment variable value", func(t *testing.T) { //nolint:paralleltest
		secretName := "empty_secret"
		envVar := secrets.EnvVarPrefix + secretName

		err := os.Setenv(envVar, "")
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		// Test retrieval - empty value should be treated as not found
		result, err := provider.GetSecret(ctx, secretName)
		assert.Error(t, err)
		assert.Empty(t, result)
		assert.Contains(t, err.Error(), "secret not found")
	})
}

func TestEnvironmentProvider_SetSecret(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()
	ctx := context.Background()

	t.Run("set secret not supported", func(t *testing.T) { //nolint:paralleltest
		err := provider.SetSecret(ctx, "test_secret", "test_value")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "environment provider is read-only")
	})

	t.Run("empty secret name", func(t *testing.T) { //nolint:paralleltest
		err := provider.SetSecret(ctx, "", "test_value")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "secret name cannot be empty")
	})
}

func TestEnvironmentProvider_DeleteSecret(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()
	ctx := context.Background()

	t.Run("delete secret not supported", func(t *testing.T) { //nolint:paralleltest
		err := provider.DeleteSecret(ctx, "test_secret")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "environment provider is read-only")
	})

	t.Run("empty secret name", func(t *testing.T) { //nolint:paralleltest
		err := provider.DeleteSecret(ctx, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "secret name cannot be empty")
	})
}

func TestEnvironmentProvider_ListSecrets(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()
	ctx := context.Background()

	t.Run("list secrets not supported", func(t *testing.T) { //nolint:paralleltest
		secrets, err := provider.ListSecrets(ctx)
		assert.Error(t, err)
		assert.Nil(t, secrets)
		assert.Contains(t, err.Error(), "environment provider does not support listing secrets for security reasons")
	})
}

func TestEnvironmentProvider_Cleanup(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()

	t.Run("cleanup is no-op", func(t *testing.T) { //nolint:paralleltest
		err := provider.Cleanup()
		assert.NoError(t, err)
	})
}

func TestEnvironmentProvider_Capabilities(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()

	t.Run("correct capabilities", func(t *testing.T) { //nolint:paralleltest
		caps := provider.Capabilities()
		assert.True(t, caps.CanRead)
		assert.False(t, caps.CanWrite)
		assert.False(t, caps.CanDelete)
		assert.False(t, caps.CanList)
		assert.False(t, caps.CanCleanup)
		assert.True(t, caps.IsReadOnly())
		assert.False(t, caps.IsReadWrite())
	})
}

func TestEnvironmentProvider_Integration(t *testing.T) { //nolint:paralleltest
	provider := secrets.NewEnvironmentProvider()
	ctx := context.Background()

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

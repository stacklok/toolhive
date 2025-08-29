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

func TestFallbackProvider_GetSecret(t *testing.T) { //nolint:paralleltest
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
		secretValue := "fallback_value"
		envVar := secrets.EnvVarPrefix + secretName

		err := os.Setenv(envVar, secretValue)
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		// Create mock primary provider that returns "not found" error
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().GetSecret(ctx, secretName).Return("", errors.New("secret not found: fallback_secret"))

		// Create fallback provider
		fallback := secrets.NewFallbackProvider(mockPrimary)

		// Test - should get value from environment fallback
		result, err := fallback.GetSecret(ctx, secretName)
		assert.NoError(t, err)
		assert.Equal(t, secretValue, result)
	})

	t.Run("primary provider not found, fallback also not found", func(t *testing.T) { //nolint:paralleltest
		secretName := "nonexistent_secret"

		// Ensure environment variable doesn't exist
		envVar := secrets.EnvVarPrefix + secretName
		os.Unsetenv(envVar)

		// Create mock primary provider that returns "not found" error
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		primaryErr := errors.New("secret not found: nonexistent_secret")
		mockPrimary.EXPECT().GetSecret(ctx, secretName).Return("", primaryErr)

		// Create fallback provider
		fallback := secrets.NewFallbackProvider(mockPrimary)

		// Test - should return original primary error
		result, err := fallback.GetSecret(ctx, secretName)
		assert.Error(t, err)
		assert.Empty(t, result)
		assert.Equal(t, primaryErr, err)
	})

	t.Run("primary provider error (not not-found), no fallback", func(t *testing.T) { //nolint:paralleltest
		secretName := "error_secret"

		// Create mock primary provider that returns a different error
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		primaryErr := errors.New("connection failed")
		mockPrimary.EXPECT().GetSecret(ctx, secretName).Return("", primaryErr)

		// Create fallback provider
		fallback := secrets.NewFallbackProvider(mockPrimary)

		// Test - should return primary error without trying fallback
		result, err := fallback.GetSecret(ctx, secretName)
		assert.Error(t, err)
		assert.Empty(t, result)
		assert.Equal(t, primaryErr, err)
	})

	t.Run("various not found error formats", func(t *testing.T) { //nolint:paralleltest
		secretName := "test_secret"
		secretValue := "fallback_value"
		envVar := secrets.EnvVarPrefix + secretName

		err := os.Setenv(envVar, secretValue)
		require.NoError(t, err)
		defer os.Unsetenv(envVar)

		testCases := []string{
			"secret not found: test_secret",
			"Secret does not exist",
			"item not found",
			"key does not exist in vault",
		}

		for _, errMsg := range testCases {
			ctrl := gomock.NewController(t)
			mockPrimary := mocks.NewMockProvider(ctrl)
			mockPrimary.EXPECT().GetSecret(ctx, secretName).Return("", errors.New(errMsg))

			fallback := secrets.NewFallbackProvider(mockPrimary)

			result, err := fallback.GetSecret(ctx, secretName)
			assert.NoError(t, err)
			assert.Equal(t, secretValue, result)
			ctrl.Finish()
		}
	})
}

func TestFallbackProvider_SetSecret(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("delegates to primary provider", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().SetSecret(ctx, "test_secret", "test_value").Return(nil)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		err := fallback.SetSecret(ctx, "test_secret", "test_value")
		assert.NoError(t, err)
	})

	t.Run("returns primary provider error", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		expectedErr := errors.New("write failed")
		mockPrimary.EXPECT().SetSecret(ctx, "test_secret", "test_value").Return(expectedErr)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		err := fallback.SetSecret(ctx, "test_secret", "test_value")
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})
}

func TestFallbackProvider_DeleteSecret(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("delegates to primary provider", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().DeleteSecret(ctx, "test_secret").Return(nil)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		err := fallback.DeleteSecret(ctx, "test_secret")
		assert.NoError(t, err)
	})

	t.Run("returns primary provider error", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		expectedErr := errors.New("delete failed")
		mockPrimary.EXPECT().DeleteSecret(ctx, "test_secret").Return(expectedErr)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		err := fallback.DeleteSecret(ctx, "test_secret")
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})
}

func TestFallbackProvider_ListSecrets(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

	t.Run("delegates to primary provider only", func(t *testing.T) { //nolint:paralleltest
		expectedSecrets := []secrets.SecretDescription{
			{Key: "secret1", Description: "First secret"},
			{Key: "secret2", Description: "Second secret"},
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().ListSecrets(ctx).Return(expectedSecrets, nil)

		// Set up environment variables that should NOT be included
		err := os.Setenv(secrets.EnvVarPrefix+"env_secret", "env_value")
		require.NoError(t, err)
		defer os.Unsetenv(secrets.EnvVarPrefix + "env_secret")

		fallback := secrets.NewFallbackProvider(mockPrimary)

		secrets, err := fallback.ListSecrets(ctx)
		assert.NoError(t, err)
		assert.Equal(t, expectedSecrets, secrets)
		// Verify environment secrets are not included
		for _, secret := range secrets {
			assert.NotEqual(t, "env_secret", secret.Key)
		}
	})

	t.Run("returns primary provider error", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		expectedErr := errors.New("list failed")
		mockPrimary.EXPECT().ListSecrets(ctx).Return(nil, expectedErr)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		secrets, err := fallback.ListSecrets(ctx)
		assert.Error(t, err)
		assert.Nil(t, secrets)
		assert.Equal(t, expectedErr, err)
	})
}

func TestFallbackProvider_Cleanup(t *testing.T) { //nolint:paralleltest
	t.Run("delegates to primary provider", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().Cleanup().Return(nil)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		err := fallback.Cleanup()
		assert.NoError(t, err)
	})

	t.Run("returns primary provider error", func(t *testing.T) { //nolint:paralleltest
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		expectedErr := errors.New("cleanup failed")
		mockPrimary.EXPECT().Cleanup().Return(expectedErr)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		err := fallback.Cleanup()
		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
	})
}

func TestFallbackProvider_Capabilities(t *testing.T) { //nolint:paralleltest
	t.Run("returns primary provider capabilities", func(t *testing.T) { //nolint:paralleltest
		expectedCaps := secrets.ProviderCapabilities{
			CanRead:    true,
			CanWrite:   true,
			CanDelete:  true,
			CanList:    true,
			CanCleanup: true,
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockPrimary := mocks.NewMockProvider(ctrl)
		mockPrimary.EXPECT().Capabilities().Return(expectedCaps)

		fallback := secrets.NewFallbackProvider(mockPrimary)

		caps := fallback.Capabilities()
		assert.Equal(t, expectedCaps, caps)
	})
}

func TestIsNotFoundError(t *testing.T) { //nolint:paralleltest
	t.Run("recognizes not found errors", func(t *testing.T) { //nolint:paralleltest
		testCases := []struct {
			err      error
			expected bool
		}{
			{nil, false},
			{errors.New("secret not found"), true},
			{errors.New("item not found"), true},
			{errors.New("key does not exist"), true},
			{errors.New("Secret does not exist"), true},
			{errors.New("connection failed"), false},
			{errors.New("invalid credentials"), false},
			{errors.New("timeout"), false},
		}

		for _, tc := range testCases {
			result := secrets.IsNotFoundError(tc.err)
			assert.Equal(t, tc.expected, result, "Error: %v", tc.err)
		}
	})
}

func TestFallbackProvider_Integration(t *testing.T) { //nolint:paralleltest
	ctx := context.Background()

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

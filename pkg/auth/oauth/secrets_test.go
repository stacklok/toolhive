package oauth

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	authsecrets "github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// TestProcessOAuthClientSecret tests the OAuth-specific wrapper function
func TestProcessOAuthClientSecret(t *testing.T) {
	t.Parallel()

	t.Run("empty client secret returns empty without accessing secrets manager", func(t *testing.T) {
		t.Parallel()

		// This test verifies that when clientSecret is empty, ProcessOAuthClientSecret
		// returns early without attempting to access the secrets manager.
		// If it tried to access the secrets manager, it would fail because
		// no secrets provider is configured in the test environment.
		result, err := ProcessOAuthClientSecret("test-workload", "")
		assert.NoError(t, err, "Should not error when client secret is empty")
		assert.Equal(t, "", result, "Should return empty string when input is empty")
	})
}

func TestProcessOAuthClientSecret_UsesCorrectPrefix(t *testing.T) {
	t.Parallel()

	t.Run("generates secret name with OAUTH_CLIENT_SECRET_ prefix", func(t *testing.T) {
		t.Parallel()

		// This test verifies that ProcessOAuthClientSecret uses the correct OAUTH_CLIENT_SECRET_ prefix
		// by testing ProcessSecretWithProvider directly with the same parameters that
		// ProcessOAuthClientSecret uses. Since ProcessOAuthClientSecret calls GetSecretsManager()
		// internally which requires secrets setup, we test the underlying function
		// with the exact parameters ProcessOAuthClientSecret would use.
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockProvider := mocks.NewMockProvider(ctrl)
		mockProvider.EXPECT().
			GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
			Return("", errors.New("secret not found"))
		mockProvider.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
		mockProvider.EXPECT().
			SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "my-client-secret").
			Return(nil)

		// Call ProcessSecretWithProvider with the exact parameters ProcessOAuthClientSecret uses
		result, err := authsecrets.ProcessSecretWithProvider("test-workload", "my-client-secret", mockProvider, "OAUTH_CLIENT_SECRET_", "oauth_secret", "OAuth client secret")
		require.NoError(t, err)

		// Parse the CLI format to verify the prefix and target
		secretParam, err := secrets.ParseSecretParameter(result)
		require.NoError(t, err)
		assert.Contains(t, secretParam.Name, "OAUTH_CLIENT_SECRET_test-workload", "Secret name should contain OAUTH_CLIENT_SECRET_ prefix")
		assert.Equal(t, "oauth_secret", secretParam.Target, "Target should be oauth_secret")
	})

	t.Run("plain text secret converts to CLI format with correct prefix", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockProvider := mocks.NewMockProvider(ctrl)
		mockProvider.EXPECT().
			GetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload").
			Return("", errors.New("secret not found"))
		mockProvider.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
		mockProvider.EXPECT().
			SetSecret(gomock.Any(), "OAUTH_CLIENT_SECRET_test-workload", "plain-text-secret-value").
			Return(nil)

		// Test with plain text secret
		result, err := authsecrets.ProcessSecretWithProvider("test-workload", "plain-text-secret-value", mockProvider, "OAUTH_CLIENT_SECRET_", "oauth_secret", "OAuth client secret")
		require.NoError(t, err)

		// Verify it's in CLI format
		secretParam, err := secrets.ParseSecretParameter(result)
		require.NoError(t, err)
		assert.Equal(t, "OAUTH_CLIENT_SECRET_test-workload", secretParam.Name, "Secret name should use OAUTH_CLIENT_SECRET_ prefix")
		assert.Equal(t, "oauth_secret", secretParam.Target, "Target should be oauth_secret")
	})
}

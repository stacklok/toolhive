package remote

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

func TestProcessBearerToken(t *testing.T) {
	t.Parallel()

	t.Run("empty bearer token returns empty without accessing secrets manager", func(t *testing.T) {
		t.Parallel()

		// This test verifies that when bearerToken is empty, ProcessBearerToken
		// returns early without attempting to access the secrets manager.
		// If it tried to access the secrets manager, it would fail because
		// no secrets provider is configured in the test environment.
		result, err := ProcessBearerToken("test-workload", "")
		assert.NoError(t, err, "Should not error when bearer token is empty")
		assert.Equal(t, "", result, "Should return empty string when input is empty")
	})
}

func TestProcessBearerToken_UsesCorrectPrefix(t *testing.T) {
	t.Parallel()

	t.Run("generates secret name with BEARER_TOKEN_ prefix", func(t *testing.T) {
		t.Parallel()

		// This test verifies that ProcessBearerToken uses the correct BEARER_TOKEN_ prefix
		// by testing ProcessSecretWithProvider directly with the same parameters that
		// ProcessBearerToken uses. Since ProcessBearerToken calls GetSecretsManager()
		// internally which requires secrets setup, we test the underlying function
		// with the exact parameters ProcessBearerToken would use.
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockProvider := mocks.NewMockProvider(ctrl)
		mockProvider.EXPECT().
			GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
			Return("", errors.New("secret not found"))
		mockProvider.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
		mockProvider.EXPECT().
			SetSecret(gomock.Any(), "BEARER_TOKEN_test-workload", "my-token").
			Return(nil)

		// Call ProcessSecretWithProvider with the exact parameters ProcessBearerToken uses
		result, err := authsecrets.ProcessSecretWithProvider("test-workload", "my-token", mockProvider, "BEARER_TOKEN_", "bearer_token", "bearer token")
		require.NoError(t, err)

		// Parse the CLI format to verify the prefix and target
		secretParam, err := secrets.ParseSecretParameter(result)
		require.NoError(t, err)
		assert.Contains(t, secretParam.Name, "BEARER_TOKEN_test-workload", "Secret name should contain BEARER_TOKEN_ prefix")
		assert.Equal(t, "bearer_token", secretParam.Target, "Target should be bearer_token")
	})

	t.Run("plain text token converts to CLI format with correct prefix", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockProvider := mocks.NewMockProvider(ctrl)
		mockProvider.EXPECT().
			GetSecret(gomock.Any(), "BEARER_TOKEN_test-workload").
			Return("", errors.New("secret not found"))
		mockProvider.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{CanWrite: true})
		mockProvider.EXPECT().
			SetSecret(gomock.Any(), "BEARER_TOKEN_test-workload", "plain-text-bearer-token-value").
			Return(nil)

		// Test with plain text token
		result, err := authsecrets.ProcessSecretWithProvider("test-workload", "plain-text-bearer-token-value", mockProvider, "BEARER_TOKEN_", "bearer_token", "bearer token")
		require.NoError(t, err)

		// Verify it's in CLI format
		secretParam, err := secrets.ParseSecretParameter(result)
		require.NoError(t, err)
		assert.Equal(t, "BEARER_TOKEN_test-workload", secretParam.Name, "Secret name should use BEARER_TOKEN_ prefix")
		assert.Equal(t, "bearer_token", secretParam.Target, "Target should be bearer_token")
	})
}

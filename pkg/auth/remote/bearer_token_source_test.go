package remote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestBearerTokenSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		bearerToken string
		expectError bool
	}{
		{
			name:        "valid bearer token",
			bearerToken: "test-token-123",
			expectError: false,
		},
		{
			name:        "empty bearer token",
			bearerToken: "",
			expectError: false,
		},
		{
			name:        "bearer token with special characters",
			bearerToken: "test-token-with-special-chars-!@#$%^&*()",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := NewBearerTokenSource(tt.bearerToken)

			token, err := source.Token()
			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotNil(t, token)
			assert.Equal(t, tt.bearerToken, token.AccessToken)
			assert.Equal(t, "Bearer", token.TokenType)
			assert.True(t, token.Expiry.IsZero(), "Bearer token should not have expiry")
		})
	}
}

func TestBearerTokenSource_Consistency(t *testing.T) {
	t.Parallel()

	source := NewBearerTokenSource("test-token")

	// Token should be consistent across multiple calls
	token1, err1 := source.Token()
	require.NoError(t, err1)

	token2, err2 := source.Token()
	require.NoError(t, err2)

	assert.Equal(t, token1.AccessToken, token2.AccessToken)
	assert.Equal(t, token1.TokenType, token2.TokenType)
}

func TestBearerTokenSource_ImplementsTokenSource(t *testing.T) {
	t.Parallel()

	// Verify that BearerTokenSource implements oauth2.TokenSource interface
	var _ oauth2.TokenSource = NewBearerTokenSource("test-token")

	tokenSource := NewBearerTokenSource("test-static-token")
	require.NotNil(t, tokenSource)

	token, err := tokenSource.Token()
	require.NoError(t, err)
	assert.Equal(t, "test-static-token", token.AccessToken)
	assert.Equal(t, "Bearer", token.TokenType)
}

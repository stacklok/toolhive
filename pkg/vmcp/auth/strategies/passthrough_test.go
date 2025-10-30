package strategies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/auth"
)

func TestPassThroughStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewPassThroughStrategy()
	assert.Equal(t, "pass_through", strategy.Name())
}

func TestPassThroughStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		setupCtx         func() context.Context
		metadata         map[string]any
		expectError      bool
		errorContains    string
		expectAuthHeader string
	}{
		{
			name: "forwards bearer token correctly",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "client-token-123",
					TokenType: "Bearer",
				})
			},
			metadata:         nil,
			expectError:      false,
			expectAuthHeader: "Bearer client-token-123",
		},
		{
			name: "handles custom token type",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "custom-token-xyz",
					TokenType: "CustomAuth",
				})
			},
			metadata:         nil,
			expectError:      false,
			expectAuthHeader: "CustomAuth custom-token-xyz",
		},
		{
			name: "defaults to Bearer when token type is empty",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "token-without-type",
					TokenType: "",
				})
			},
			metadata:         nil,
			expectError:      false,
			expectAuthHeader: "Bearer token-without-type",
		},
		{
			name: "metadata is ignored",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "token-456",
					TokenType: "Bearer",
				})
			},
			metadata:         map[string]any{"ignored": "value", "also_ignored": 123},
			expectError:      false,
			expectAuthHeader: "Bearer token-456",
		},
		{
			name: "preserves JWT token type",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test",
					TokenType: "JWT",
				})
			},
			metadata:         nil,
			expectError:      false,
			expectAuthHeader: "JWT eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test",
		},
		{
			name: "returns error when no identity in context",
			setupCtx: func() context.Context {
				return context.Background() // No identity
			},
			metadata:      nil,
			expectError:   true,
			errorContains: "no identity",
		},
		{
			name: "returns error when identity has empty token",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "",
					TokenType: "Bearer",
				})
			},
			metadata:      nil,
			expectError:   true,
			errorContains: "no token",
		},
		{
			name: "returns error when identity is nil",
			setupCtx: func() context.Context {
				// WithIdentity returns original context if identity is nil
				return auth.WithIdentity(context.Background(), nil)
			},
			metadata:      nil,
			expectError:   true,
			errorContains: "no identity",
		},
		{
			name: "handles long tokens",
			setupCtx: func() context.Context {
				longToken := "very-long-token-" + string(make([]byte, 1000))
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     longToken,
					TokenType: "Bearer",
				})
			},
			metadata:         nil,
			expectError:      false,
			expectAuthHeader: "Bearer very-long-token-" + string(make([]byte, 1000)),
		},
		{
			name: "handles special characters in token",
			setupCtx: func() context.Context {
				return auth.WithIdentity(context.Background(), &auth.Identity{
					Token:     "token-with-special-chars-!@#$%^&*()",
					TokenType: "Bearer",
				})
			},
			metadata:         nil,
			expectError:      false,
			expectAuthHeader: "Bearer token-with-special-chars-!@#$%^&*()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewPassThroughStrategy()
			ctx := tt.setupCtx()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			err := strategy.Authenticate(ctx, req, tt.metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectAuthHeader, req.Header.Get("Authorization"))
		})
	}
}

func TestPassThroughStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		metadata    map[string]any
		expectError bool
	}{
		{
			name:        "nil metadata is valid",
			metadata:    nil,
			expectError: false,
		},
		{
			name:        "empty metadata is valid",
			metadata:    map[string]any{},
			expectError: false,
		},
		{
			name:        "any metadata is valid (ignored)",
			metadata:    map[string]any{"anything": "goes", "count": 123},
			expectError: false,
		},
		{
			name: "complex metadata is valid (ignored)",
			metadata: map[string]any{
				"nested": map[string]any{
					"key": "value",
				},
				"array": []string{"a", "b", "c"},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewPassThroughStrategy()
			err := strategy.Validate(tt.metadata)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

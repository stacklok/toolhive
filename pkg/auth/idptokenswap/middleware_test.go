package idptokenswap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
)

// Test constants
const (
	testSessionID = "test-session-123"
)

func TestCreateIDPTokenSwapMiddleware_HappyPath(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	// Setup expectations
	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
		ClientID:    "client-456",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	// Create middleware
	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	// Create a test handler that captures the request
	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	// Create request with identity in context
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":       "user-123",
			"azp":       "client-456",
			"tsid":      sessionID,
			"client_id": "client-456",
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	// Execute
	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Verify
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer idp-access-token-xyz", capturedAuthHeader)
}

func TestCreateIDPTokenSwapMiddleware_NoIdentityInContext(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)
	// Storage should NOT be called when there's no identity

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	// Create a test handler that verifies it was called
	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	// Create request WITHOUT identity in context
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Verify next handler was called (pass-through behavior)
	assert.True(t, nextCalled, "next handler should be called when no identity in context")
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestCreateIDPTokenSwapMiddleware_MissingSessionIDClaim(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)
	// Storage should NOT be called when tsid is missing

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	// Create request with identity but NO tsid claim
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub": "user-123",
			// Note: no "tsid" claim
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Verify error response
	assert.False(t, nextCalled, "next handler should not be called when tsid is missing")
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "Missing session identifier")
}

func TestCreateIDPTokenSwapMiddleware_EmptySessionIDClaim(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	// Create request with empty tsid claim
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"tsid": "", // empty string
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "Missing session identifier")
}

func TestCreateIDPTokenSwapMiddleware_StorageError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(nil, errors.New("storage error"))

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "Session not found")
}

func TestCreateIDPTokenSwapMiddleware_SubjectMismatch(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "different-user-456", // Different from JWT sub
		ClientID:    "client-456",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123", // Does not match idpTokens.Subject
			"azp":  "client-456",
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Session binding mismatch")
}

func TestCreateIDPTokenSwapMiddleware_ClientIDMismatch(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
		ClientID:    "different-client-789", // Different from JWT azp/client_id
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"azp":  "client-456", // Does not match idpTokens.ClientID
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Session binding mismatch")
}

func TestCreateIDPTokenSwapMiddleware_ClientIDFromClientIDClaim(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
		ClientID:    "client-456",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	// Use client_id instead of azp
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":       "user-123",
			"client_id": "client-456", // Using client_id instead of azp
			"tsid":      sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer idp-access-token-xyz", capturedAuthHeader)
}

func TestCreateIDPTokenSwapMiddleware_TokenExpired(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(-time.Hour), // Expired
		Subject:     "user-123",
		ClientID:    "client-456",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"azp":  "client-456",
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "Token expired")
}

func TestCreateIDPTokenSwapMiddleware_BindingVerificationDisabled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "different-user-456", // Mismatch should be ignored
		ClientID:    "different-client-789",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	// Disable binding verification
	verifyBinding := false
	config := Config{
		Storage:       mockStorage,
		VerifyBinding: &verifyBinding,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123", // Mismatches idpTokens.Subject but binding is disabled
			"azp":  "client-456",
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Should succeed despite mismatch because binding verification is disabled
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer idp-access-token-xyz", capturedAuthHeader)
}

func TestCreateIDPTokenSwapMiddleware_CustomSessionIDClaimKey(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
		ClientID:    "client-456",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	// Use custom claim key
	config := Config{
		Storage:           mockStorage,
		SessionIDClaimKey: "custom_session_id",
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":               "user-123",
			"azp":               "client-456",
			"custom_session_id": sessionID, // Using custom claim key
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer idp-access-token-xyz", capturedAuthHeader)
}

func TestCreateIDPTokenSwapMiddleware_EmptySubjectInStoredTokens(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "", // Empty - should skip validation
		ClientID:    "client-456",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"azp":  "client-456",
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Should succeed - empty Subject in stored tokens means skip validation
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer idp-access-token-xyz", capturedAuthHeader)
}

func TestCreateIDPTokenSwapMiddleware_EmptyClientIDInStoredTokens(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
		ClientID:    "", // Empty - should skip validation
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	var capturedAuthHeader string
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"azp":  "any-client-id",
			"tsid": sessionID,
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Should succeed - empty ClientID in stored tokens means skip validation
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "Bearer idp-access-token-xyz", capturedAuthHeader)
}

func TestCreateIDPTokenSwapMiddleware_NoClientIDInJWTClaims(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := testSessionID
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-access-token-xyz",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
		ClientID:    "expected-client", // Expects a client ID
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	nextCalled := false
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})

	// No azp or client_id in claims
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"tsid": sessionID,
			// No azp or client_id
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	// Should fail because stored ClientID is non-empty but JWT has no client_id
	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Session binding mismatch")
}

func TestVerifyTokenBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		identity   *auth.Identity
		idpTokens  *storage.IDPTokens
		expectErr  bool
		errContain string
	}{
		{
			name: "matching subject and client_id",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub": "user-123",
					"azp": "client-456",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "user-123",
				ClientID: "client-456",
			},
			expectErr: false,
		},
		{
			name: "subject mismatch",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub": "user-123",
					"azp": "client-456",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "different-user",
				ClientID: "client-456",
			},
			expectErr:  true,
			errContain: "subject mismatch",
		},
		{
			name: "client_id mismatch",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub": "user-123",
					"azp": "client-456",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "user-123",
				ClientID: "different-client",
			},
			expectErr:  true,
			errContain: "client_id mismatch",
		},
		{
			name: "empty stored subject allows any",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub": "any-user",
					"azp": "client-456",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "",
				ClientID: "client-456",
			},
			expectErr: false,
		},
		{
			name: "empty stored client_id allows any",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub": "user-123",
					"azp": "any-client",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "user-123",
				ClientID: "",
			},
			expectErr: false,
		},
		{
			name: "client_id from client_id claim",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub":       "user-123",
					"client_id": "client-456",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "user-123",
				ClientID: "client-456",
			},
			expectErr: false,
		},
		{
			name: "azp takes precedence over client_id",
			identity: &auth.Identity{
				Claims: map[string]any{
					"sub":       "user-123",
					"azp":       "azp-client",
					"client_id": "client_id-client",
				},
			},
			idpTokens: &storage.IDPTokens{
				Subject:  "user-123",
				ClientID: "azp-client", // Should match azp, not client_id
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := verifyTokenBinding(tt.identity, tt.idpTokens, "test-session")

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContain)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultValues(t *testing.T) {
	t.Parallel()

	// Test that default session ID claim key is used
	assert.Equal(t, "tsid", DefaultSessionIDClaimKey)

	// Test that middleware type is correct
	assert.Equal(t, "idp-token-swap", MiddlewareType)
}

func TestConfig_Defaults(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	// Create config with minimal settings
	config := Config{
		Storage: mockStorage,
	}

	// Verify that the middleware uses default values
	// We can't directly test the internal state, but we can verify behavior

	// Test 1: Default session ID claim key should be "tsid"
	sessionID := "test-session"
	idpTokens := &storage.IDPTokens{
		AccessToken: "token",
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	middleware := CreateIDPTokenSwapMiddleware(config)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Claims: map[string]any{
			"sub":  "user",
			"tsid": sessionID, // Using default "tsid" key
		},
	}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	rr := httptest.NewRecorder()
	nextCalled := false
	middleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		nextCalled = true
	})).ServeHTTP(rr, req)

	assert.True(t, nextCalled, "middleware should work with default tsid claim key")
}

func TestMiddleware_PreservesRequestContext(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStorage := mocks.NewMockIDPTokenStorage(ctrl)

	sessionID := "test-session"
	idpTokens := &storage.IDPTokens{
		AccessToken: "idp-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		Subject:     "user-123",
	}

	mockStorage.EXPECT().
		GetIDPTokens(gomock.Any(), sessionID).
		Return(idpTokens, nil)

	config := Config{
		Storage: mockStorage,
	}
	middleware := CreateIDPTokenSwapMiddleware(config)

	// Add custom value to context
	type contextKey string
	customKey := contextKey("custom")
	var capturedCustomValue string

	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(customKey).(string); ok {
			capturedCustomValue = v
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	identity := &auth.Identity{
		Subject: "user-123",
		Claims: map[string]any{
			"sub":  "user-123",
			"tsid": sessionID,
		},
	}
	ctx := auth.WithIdentity(req.Context(), identity)
	ctx = context.WithValue(ctx, customKey, "custom-value")
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	middleware(nextHandler).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "custom-value", capturedCustomValue, "middleware should preserve request context")
}

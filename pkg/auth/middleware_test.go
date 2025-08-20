package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/transport/types/mocks"
)

func TestMiddleware_Handler(t *testing.T) {
	t.Parallel()

	// Create a mock middleware function
	mockMiddlewareFunc := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test", "middleware-called")
			next.ServeHTTP(w, r)
		})
	}

	// Create middleware instance
	middleware := &Middleware{
		middleware: mockMiddlewareFunc,
	}

	// Test that Handler returns the correct middleware function
	handlerFunc := middleware.Handler()

	// Create a test handler to wrap
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap the test handler with the middleware
	wrappedHandler := handlerFunc(testHandler)

	// Test the wrapped handler
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	wrappedHandler.ServeHTTP(w, req)

	// Verify the middleware was called
	assert.Equal(t, "middleware-called", w.Header().Get("X-Test"))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "test response", w.Body.String())
}

func TestMiddleware_Close(t *testing.T) {
	t.Parallel()

	middleware := &Middleware{}

	// Test that Close returns nil (no cleanup needed)
	err := middleware.Close()
	assert.NoError(t, err)
}

func TestMiddleware_AuthInfoHandler(t *testing.T) {
	t.Parallel()

	// Create a mock auth info handler
	mockHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("auth info"))
	})

	middleware := &Middleware{
		authInfoHandler: mockHandler,
	}

	// Test that AuthInfoHandler returns the correct handler
	handler := middleware.AuthInfoHandler()

	// Test the handler
	req := httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "auth info", w.Body.String())
}

func TestCreateMiddleware_WithoutOIDCConfig(t *testing.T) {
	t.Parallel()

	// Initialize logger for testing
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock runner
	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	// Expect AddMiddleware to be called with a middleware instance
	mockRunner.EXPECT().AddMiddleware(gomock.Any()).Do(func(mw types.Middleware) {
		// Verify it's our auth middleware
		_, ok := mw.(*Middleware)
		assert.True(t, ok, "Expected middleware to be of type *auth.Middleware")
	})

	// Create parameters without OIDC config (local auth)
	params := MiddlewareParams{}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: paramsJSON,
	}

	// Test CreateMiddleware
	err = CreateMiddleware(config, mockRunner)
	assert.NoError(t, err)
}

func TestCreateMiddleware_WithOIDCConfig(t *testing.T) {
	t.Skip("Skipping OIDC test - requires real OIDC discovery endpoint or complex mocking")
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock runner
	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	// Create parameters with OIDC config
	oidcConfig := &TokenValidatorConfig{
		Issuer:      "https://example.com/auth",
		ResourceURL: "https://api.example.com",
	}
	params := MiddlewareParams{
		OIDCConfig: oidcConfig,
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: paramsJSON,
	}

	// Note: This test is skipped because NewTokenValidator requires actual OIDC discovery
	// In a real test environment, you'd need to mock the OIDC discovery or use a test OIDC server
	err = CreateMiddleware(config, mockRunner)

	// We expect an error here because we don't have a real OIDC endpoint
	// The important thing is that it gets past parameter parsing
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create authentication middleware")
}

func TestCreateMiddleware_InvalidParameters(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	// Create config with invalid JSON parameters
	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: []byte(`{"invalid": json`), // Invalid JSON
	}

	// Test CreateMiddleware with invalid parameters
	err := CreateMiddleware(config, mockRunner)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal auth middleware parameters")
}

func TestCreateMiddleware_NilParameters(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	// Create config with nil parameters - this should fail during unmarshaling
	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: nil,
	}

	// This should fail because nil cannot be unmarshaled
	err := CreateMiddleware(config, mockRunner)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal auth middleware parameters")
}

func TestCreateMiddleware_EmptyParameters(t *testing.T) {
	t.Parallel()

	// Initialize logger for testing
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRunner := mocks.NewMockMiddlewareRunner(ctrl)

	// Expect AddMiddleware to be called
	mockRunner.EXPECT().AddMiddleware(gomock.Any())

	// Create config with empty JSON parameters
	config := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: []byte(`{}`),
	}

	err := CreateMiddleware(config, mockRunner)
	assert.NoError(t, err)
}

func TestMiddlewareType_Constant(t *testing.T) {
	t.Parallel()

	// Test that the middleware type constant is correct
	assert.Equal(t, "auth", MiddlewareType)
}

func TestMiddleware_InterfaceCompliance(t *testing.T) {
	t.Parallel()

	// Test that Middleware implements the types.Middleware interface
	var _ types.Middleware = (*Middleware)(nil)
}

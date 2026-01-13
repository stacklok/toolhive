package recovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecoveryMiddleware_NoPanic(t *testing.T) {
	t.Parallel()

	// Create a test handler that does not panic
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("success"))
	})

	// Wrap with recovery middleware
	wrappedHandler := Middleware(testHandler)

	// Create test request
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	// Execute request
	wrappedHandler.ServeHTTP(rec, req)

	// Verify response
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "success", rec.Body.String())
}

func TestRecoveryMiddleware_RecoverFromPanic(t *testing.T) {
	t.Parallel()

	// Create a test handler that panics
	testHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})

	// Wrap with recovery middleware
	wrappedHandler := Middleware(testHandler)

	// Create test request
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	// Execute request - should not panic
	wrappedHandler.ServeHTTP(rec, req)

	// Verify 500 Internal Server Error response
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "Internal Server Error")
}

func TestRecoveryMiddleware_PreservesRequestContext(t *testing.T) {
	t.Parallel()

	type contextKey string
	const key contextKey = "test-key"
	const value = "test-value"

	var receivedValue string

	// Create a test handler that reads from context
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Context().Value(key); v != nil {
			receivedValue = v.(string)
		}
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with recovery middleware
	wrappedHandler := Middleware(testHandler)

	// Create test request with context value
	req := httptest.NewRequest("GET", "/test", nil)
	ctx := context.WithValue(req.Context(), key, value)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	// Execute request
	wrappedHandler.ServeHTTP(rec, req)

	// Verify context was preserved
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, value, receivedValue)
}

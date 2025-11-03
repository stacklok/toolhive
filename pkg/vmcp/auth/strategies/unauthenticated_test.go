package strategies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnauthenticatedStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewUnauthenticatedStrategy()
	assert.Equal(t, "unauthenticated", strategy.Name())
}

func TestUnauthenticatedStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		metadata     map[string]any
		setupRequest func() *http.Request
		checkRequest func(t *testing.T, req *http.Request)
	}{
		{
			name:     "does not modify request with no metadata",
			metadata: nil,
			setupRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
				req.Header.Set("X-Custom-Header", "original-value")
				return req
			},
			checkRequest: func(t *testing.T, req *http.Request) {
				t.Helper()
				// Original headers should be unchanged
				assert.Equal(t, "original-value", req.Header.Get("X-Custom-Header"))
				// No auth headers should be added
				assert.Empty(t, req.Header.Get("Authorization"))
			},
		},
		{
			name: "does not modify request with metadata present",
			metadata: map[string]any{
				"some_key": "some_value",
				"count":    42,
			},
			setupRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
				req.Header.Set("X-Existing", "existing-value")
				return req
			},
			checkRequest: func(t *testing.T, req *http.Request) {
				t.Helper()
				// Original headers should be unchanged
				assert.Equal(t, "existing-value", req.Header.Get("X-Existing"))
				// No auth headers should be added
				assert.Empty(t, req.Header.Get("Authorization"))
			},
		},
		{
			name:     "preserves existing Authorization header",
			metadata: nil,
			setupRequest: func() *http.Request {
				req := httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
				req.Header.Set("Authorization", "Bearer existing-token")
				return req
			},
			checkRequest: func(t *testing.T, req *http.Request) {
				t.Helper()
				// Should not modify existing Authorization header
				assert.Equal(t, "Bearer existing-token", req.Header.Get("Authorization"))
			},
		},
		{
			name:     "works with empty request",
			metadata: nil,
			setupRequest: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
			},
			checkRequest: func(t *testing.T, req *http.Request) {
				t.Helper()
				// Request should have no auth headers
				assert.Empty(t, req.Header.Get("Authorization"))
				// Headers should be empty or minimal
				assert.LessOrEqual(t, len(req.Header), 1) // May have Host header
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewUnauthenticatedStrategy()
			req := tt.setupRequest()
			ctx := context.Background()

			err := strategy.Authenticate(ctx, req, tt.metadata)

			require.NoError(t, err)
			tt.checkRequest(t, req)
		})
	}
}

func TestUnauthenticatedStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{
			name:     "accepts nil metadata",
			metadata: nil,
		},
		{
			name:     "accepts empty metadata",
			metadata: map[string]any{},
		},
		{
			name: "accepts arbitrary metadata",
			metadata: map[string]any{
				"key1":   "value1",
				"key2":   42,
				"key3":   []string{"a", "b", "c"},
				"nested": map[string]any{"inner": "value"},
			},
		},
		{
			name: "accepts metadata with typical auth fields",
			metadata: map[string]any{
				"token_url":   "https://example.com/token",
				"client_id":   "client-123",
				"header_name": "X-API-Key",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewUnauthenticatedStrategy()
			err := strategy.Validate(tt.metadata)

			require.NoError(t, err)
		})
	}
}

func TestUnauthenticatedStrategy_IntegrationBehavior(t *testing.T) {
	t.Parallel()

	t.Run("strategy can be called multiple times safely", func(t *testing.T) {
		t.Parallel()

		strategy := NewUnauthenticatedStrategy()
		ctx := context.Background()

		// Call multiple times with different requests
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
			err := strategy.Authenticate(ctx, req, nil)
			require.NoError(t, err)
			assert.Empty(t, req.Header.Get("Authorization"))
		}
	})

	t.Run("strategy is safe for concurrent use", func(t *testing.T) {
		t.Parallel()

		strategy := NewUnauthenticatedStrategy()
		ctx := context.Background()

		// Run authentication concurrently
		done := make(chan bool, 10)
		for i := 0; i < 10; i++ {
			go func() {
				req := httptest.NewRequest(http.MethodGet, "http://backend.example.com/test", nil)
				err := strategy.Authenticate(ctx, req, nil)
				assert.NoError(t, err)
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

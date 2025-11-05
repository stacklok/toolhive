package strategies

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderInjectionStrategy_Name(t *testing.T) {
	t.Parallel()

	strategy := NewHeaderInjectionStrategy()
	assert.Equal(t, "header_injection", strategy.Name())
}

func TestHeaderInjectionStrategy_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		metadata      map[string]any
		expectError   bool
		errorContains string
		checkHeader   func(t *testing.T, req *http.Request)
	}{
		{
			name: "sets X-API-Key header correctly",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "secret-key-123",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "secret-key-123", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "sets Authorization header with API key",
			metadata: map[string]any{
				"header_name":  "Authorization",
				"header_value": "ApiKey my-secret-key",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "ApiKey my-secret-key", req.Header.Get("Authorization"))
			},
		},
		{
			name: "sets custom header name",
			metadata: map[string]any{
				"header_name":  "X-Custom-Auth-Token",
				"header_value": "custom-token-value",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "custom-token-value", req.Header.Get("X-Custom-Auth-Token"))
			},
		},
		{
			name: "handles complex header values",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.test",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.test",
					req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "handles header value with special characters",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "key-with-!@#$%^&*()-_=+[]{}|;:,.<>?",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "key-with-!@#$%^&*()-_=+[]{}|;:,.<>?", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "ignores additional metadata fields",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "my-key",
				"extra_field":  "ignored",
				"another":      123,
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				assert.Equal(t, "my-key", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "returns error when header_name is missing",
			metadata: map[string]any{
				"header_value": "my-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_name is empty string",
			metadata: map[string]any{
				"header_name":  "",
				"header_value": "my-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_name is not a string",
			metadata: map[string]any{
				"header_name":  123,
				"header_value": "my-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_value is missing",
			metadata: map[string]any{
				"header_name": "X-API-Key",
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name: "returns error when api_key is empty string",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "",
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name: "returns error when header_value is not a string",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": 123,
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name:          "returns error when metadata is nil",
			metadata:      nil,
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name:          "returns error when metadata is empty",
			metadata:      map[string]any{},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when both fields are missing",
			metadata: map[string]any{
				"unrelated": "field",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "overwrites existing header value",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "new-key",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				// Verify the new key was set (old-key was already set before Authenticate)
				assert.Equal(t, "new-key", req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "handles very long header values",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": string(make([]byte, 10000)) + "very-long-key",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				expected := string(make([]byte, 10000)) + "very-long-key"
				assert.Equal(t, expected, req.Header.Get("X-API-Key"))
			},
		},
		{
			name: "handles case-sensitive header names",
			metadata: map[string]any{
				"header_name":  "x-api-key", // lowercase
				"header_value": "my-key",
			},
			expectError: false,
			checkHeader: func(t *testing.T, req *http.Request) {
				t.Helper()
				// HTTP headers are case-insensitive, but Go normalizes them
				assert.Equal(t, "my-key", req.Header.Get("x-api-key"))
				assert.Equal(t, "my-key", req.Header.Get("X-Api-Key"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewHeaderInjectionStrategy()
			ctx := context.Background()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)

			// Special setup for the "overwrites existing header value" test
			if tt.name == "overwrites existing header value" {
				req.Header.Set("X-API-Key", "old-key")
			}

			err := strategy.Authenticate(ctx, req, tt.metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			if tt.checkHeader != nil {
				tt.checkHeader(t, req)
			}
		})
	}
}

func TestHeaderInjectionStrategy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		metadata      map[string]any
		expectError   bool
		errorContains string
	}{
		{
			name: "valid metadata with all required fields",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "secret-key",
			},
			expectError: false,
		},
		{
			name: "valid with extra metadata fields",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "secret-key",
				"extra":        "ignored",
				"count":        123,
			},
			expectError: false,
		},
		{
			name: "valid with different header name",
			metadata: map[string]any{
				"header_name":  "Authorization",
				"header_value": "Bearer token",
			},
			expectError: false,
		},
		{
			name: "returns error when header_name is missing",
			metadata: map[string]any{
				"header_value": "secret-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_name is empty",
			metadata: map[string]any{
				"header_name":  "",
				"header_value": "secret-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_name is not a string",
			metadata: map[string]any{
				"header_name":  123,
				"header_value": "secret-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_name is a boolean",
			metadata: map[string]any{
				"header_name":  true,
				"header_value": "secret-key",
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when header_value is missing",
			metadata: map[string]any{
				"header_name": "X-API-Key",
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name: "returns error when header_value is empty",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "",
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name: "returns error when header_value is not a string",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": 123,
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name: "returns error when header_value is a map",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": map[string]any{"nested": "value"},
			},
			expectError:   true,
			errorContains: "header_value required",
		},
		{
			name:          "returns error when metadata is nil",
			metadata:      nil,
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name:          "returns error when metadata is empty",
			metadata:      map[string]any{},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error when both fields are wrong type",
			metadata: map[string]any{
				"header_name":  123,
				"header_value": false,
			},
			expectError:   true,
			errorContains: "header_name required",
		},
		{
			name: "returns error for whitespace in header_name",
			metadata: map[string]any{
				"header_name":  "X-Custom Header",
				"header_value": "key",
			},
			expectError:   true,
			errorContains: "invalid header_name",
		},
		{
			name: "accepts unicode in header_value",
			metadata: map[string]any{
				"header_name":  "X-API-Key",
				"header_value": "key-with-unicode-日本語",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			strategy := NewHeaderInjectionStrategy()
			err := strategy.Validate(tt.metadata)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

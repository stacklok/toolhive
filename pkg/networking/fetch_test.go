// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package networking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testResponse is a sample response type for testing.
type testResponse struct {
	Message string `json:"message"`
	Value   int    `json:"value"`
}

func TestFetchJSON_SuccessfulGET(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "test-value")
		_ = json.NewEncoder(w).Encode(testResponse{Message: "hello", Value: 42})
	}))
	defer server.Close()

	ctx := context.Background()
	client := server.Client()

	result, err := FetchJSON[testResponse](ctx, client, server.URL)
	require.NoError(t, err)

	assert.Equal(t, "hello", result.Data.Message)
	assert.Equal(t, 42, result.Data.Value)
	assert.Equal(t, "test-value", result.Headers.Get("X-Custom-Header"))
}

func TestFetchJSON_SuccessfulPOST(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testResponse{Message: "created", Value: 1})
	}))
	defer server.Close()

	ctx := context.Background()
	client := server.Client()

	body := strings.NewReader(`{"input": "test"}`)
	result, err := FetchJSON[testResponse](ctx, client, server.URL,
		WithMethod(http.MethodPost),
		WithHeader("Content-Type", "application/json"),
		WithBody(body),
	)
	require.NoError(t, err)

	assert.Equal(t, "created", result.Data.Message)
	assert.Equal(t, 1, result.Data.Value)
}

func TestFetchJSONWithForm_Success(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

		err := r.ParseForm()
		require.NoError(t, err)
		assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		assert.Equal(t, "test-code", r.Form.Get("code"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testResponse{Message: "token", Value: 3600})
	}))
	defer server.Close()

	ctx := context.Background()
	client := server.Client()

	formData := url.Values{
		"grant_type": {"authorization_code"},
		"code":       {"test-code"},
	}

	result, err := FetchJSONWithForm[testResponse](ctx, client, server.URL, formData)
	require.NoError(t, err)

	assert.Equal(t, "token", result.Data.Message)
	assert.Equal(t, 3600, result.Data.Value)
}

func TestFetchJSON_HTTPError4xx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		statusCode     int
		expectedStatus string
	}{
		{"bad request", http.StatusBadRequest, "400 Bad Request"},
		{"unauthorized", http.StatusUnauthorized, "401 Unauthorized"},
		{"forbidden", http.StatusForbidden, "403 Forbidden"},
		{"not found", http.StatusNotFound, "404 Not Found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				// Write some body content that should NOT appear in error message
				_, _ = w.Write([]byte("sensitive error details"))
			}))
			defer server.Close()

			ctx := context.Background()
			client := server.Client()

			result, err := FetchJSON[testResponse](ctx, client, server.URL)
			assert.Nil(t, result)
			require.Error(t, err)

			var httpErr *HTTPError
			require.True(t, errors.As(err, &httpErr))
			assert.Equal(t, tt.statusCode, httpErr.StatusCode)
			// Error message should be HTTP status text, not body content
			assert.Equal(t, tt.expectedStatus, httpErr.Message)
			assert.Equal(t, server.URL, httpErr.URL)
			// Verify body content is not leaked
			assert.NotContains(t, httpErr.Message, "sensitive")
		})
	}
}

func TestFetchJSON_HTTPError5xx(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{"internal server error", http.StatusInternalServerError},
		{"bad gateway", http.StatusBadGateway},
		{"service unavailable", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte("server error"))
			}))
			defer server.Close()

			ctx := context.Background()
			client := server.Client()

			result, err := FetchJSON[testResponse](ctx, client, server.URL)
			assert.Nil(t, result)
			require.Error(t, err)

			assert.True(t, IsHTTPError(err, tt.statusCode))
		})
	}
}

func TestFetchJSON_ContentTypeValidation(t *testing.T) {
	t.Parallel()

	t.Run("valid content type", func(t *testing.T) {
		t.Parallel()

		contentTypes := []string{
			"application/json",
			"application/json; charset=utf-8",
			"APPLICATION/JSON",
			"application/json;charset=UTF-8",
		}

		for _, ct := range contentTypes {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				_ = json.NewEncoder(w).Encode(testResponse{Message: "ok"})
			}))

			ctx := context.Background()
			result, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

			require.NoError(t, err, "content type %q should be valid", ct)
			assert.Equal(t, "ok", result.Data.Message)

			server.Close()
		}
	})

	t.Run("invalid content type", func(t *testing.T) {
		t.Parallel()

		invalidContentTypes := []string{
			"text/plain",
			"text/html",
			"application/xml",
			"",
		}

		for _, ct := range invalidContentTypes {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				_ = json.NewEncoder(w).Encode(testResponse{Message: "ok"})
			}))

			ctx := context.Background()
			_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

			require.Error(t, err, "content type %q should be invalid", ct)
			assert.Contains(t, err.Error(), "unexpected content type")

			server.Close()
		}
	})
}

func TestFetchJSON_ErrorDoesNotLeakBody(t *testing.T) {
	t.Parallel()

	// Even with a large body containing sensitive data, the error should only show status text
	largeBody := strings.Repeat("sensitive-data-", 500)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(largeBody))
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

	require.Error(t, err)
	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	// Error message should be HTTP status text, not body content
	assert.Equal(t, "400 Bad Request", httpErr.Message)
	assert.NotContains(t, httpErr.Message, "sensitive")
}

func TestFetchJSON_CustomHeaders(t *testing.T) {
	t.Parallel()

	t.Run("single header", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testResponse{Message: "authenticated"})
		}))
		defer server.Close()

		ctx := context.Background()
		result, err := FetchJSON[testResponse](ctx, server.Client(), server.URL,
			WithHeader("Authorization", "Bearer test-token"),
		)

		require.NoError(t, err)
		assert.Equal(t, "authenticated", result.Data.Message)
	})

	t.Run("multiple headers", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer token", r.Header.Get("Authorization"))
			assert.Equal(t, "custom-value", r.Header.Get("X-Custom"))
			assert.Equal(t, "request-123", r.Header.Get("X-Request-ID"))

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testResponse{Message: "ok"})
		}))
		defer server.Close()

		ctx := context.Background()
		result, err := FetchJSON[testResponse](ctx, server.Client(), server.URL,
			WithHeader("Authorization", "Bearer token"),
			WithHeader("X-Custom", "custom-value"),
			WithHeader("X-Request-ID", "request-123"),
		)

		require.NoError(t, err)
		assert.Equal(t, "ok", result.Data.Message)
	})

	t.Run("override Accept header", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Custom Accept header should override the default
			assert.Equal(t, "application/vnd.api+json", r.Header.Get("Accept"))

			// Server responds with JSON content type (validated by FetchJSON)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testResponse{Message: "custom"})
		}))
		defer server.Close()

		ctx := context.Background()
		result, err := FetchJSON[testResponse](ctx, server.Client(), server.URL,
			WithHeader("Accept", "application/vnd.api+json"),
		)

		require.NoError(t, err)
		assert.Equal(t, "custom", result.Data.Message)
	})
}

func TestFetchJSON_CustomErrorHandler(t *testing.T) {
	t.Parallel()

	// oauthError represents an OAuth error response
	type oauthError struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	t.Run("error handler returns custom error", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(oauthError{
				Error:            "invalid_grant",
				ErrorDescription: "The authorization code has expired",
			})
		}))
		defer server.Close()

		customHandler := func(_ *http.Response, body []byte) error {
			var oauthErr oauthError
			if err := json.Unmarshal(body, &oauthErr); err == nil && oauthErr.Error != "" {
				return fmt.Errorf("oauth error: %s - %s", oauthErr.Error, oauthErr.ErrorDescription)
			}
			return nil // Fall back to default HTTPError
		}

		ctx := context.Background()
		_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL,
			WithErrorHandler(customHandler),
		)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid_grant")
		assert.Contains(t, err.Error(), "The authorization code has expired")
		// Should NOT be an HTTPError since custom handler returned an error
		assert.False(t, IsHTTPError(err, 0))
	})

	t.Run("error handler returns nil falls back to HTTPError", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal error"))
		}))
		defer server.Close()

		customHandler := func(_ *http.Response, _ []byte) error {
			// Return nil to fall back to default HTTPError
			return nil
		}

		ctx := context.Background()
		_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL,
			WithErrorHandler(customHandler),
		)

		require.Error(t, err)
		assert.True(t, IsHTTPError(err, http.StatusInternalServerError))
	})
}

func TestFetchJSON_ContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Delay response to allow cancellation
			time.Sleep(100 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testResponse{Message: "too late"})
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
	})

	t.Run("context timeout", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Delay response longer than timeout
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(testResponse{Message: "too late"})
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

		require.Error(t, err)
		assert.True(t, errors.Is(err, context.DeadlineExceeded))
	})
}

func TestFetchJSON_InvalidJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse JSON")
}

func TestFetchJSON_EmptyResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ctx := context.Background()
	result, err := FetchJSON[testResponse](ctx, server.Client(), server.URL)

	require.NoError(t, err)
	assert.Equal(t, "", result.Data.Message)
	assert.Equal(t, 0, result.Data.Value)
}

func TestFetchJSON_InvalidURL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &http.Client{}

	_, err := FetchJSON[testResponse](ctx, client, "://invalid-url")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create request")
}

func TestFetchJSON_NetworkError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &http.Client{Timeout: 100 * time.Millisecond}

	// Use a URL that will fail to connect
	_, err := FetchJSON[testResponse](ctx, client, "http://localhost:1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "request failed")
}

func TestFetchJSONWithForm_AdditionalOptions(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testResponse{Message: "with auth"})
	}))
	defer server.Close()

	ctx := context.Background()
	formData := url.Values{"key": {"value"}}

	result, err := FetchJSONWithForm[testResponse](ctx, server.Client(), server.URL, formData,
		WithHeader("Authorization", "Bearer token"),
	)

	require.NoError(t, err)
	assert.Equal(t, "with auth", result.Data.Message)
}

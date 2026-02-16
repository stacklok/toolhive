// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name: "valid config",
			config: Config{
				Name:          "test",
				URL:           "https://example.com/webhook",
				Timeout:       5 * time.Second,
				FailurePolicy: FailurePolicyFail,
			},
			expectError: false,
		},
		{
			name: "valid config with zero timeout",
			config: Config{
				Name:          "test",
				URL:           "https://example.com/webhook",
				Timeout:       0,
				FailurePolicy: FailurePolicyIgnore,
			},
			expectError: false,
		},
		{
			name: "invalid config",
			config: Config{
				Name: "",
				URL:  "",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(tt.config, TypeValidating, nil)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestClientCallValidating(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		serverHandler  http.HandlerFunc
		expectError    bool
		expectedResult *Response
		errorType      interface{}
	}{
		{
			name: "allowed response",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				resp := Response{
					Version: APIVersion,
					UID:     "test-uid",
					Allowed: true,
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			},
			expectedResult: &Response{
				Version: APIVersion,
				UID:     "test-uid",
				Allowed: true,
			},
		},
		{
			name: "denied response",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				resp := Response{
					Version: APIVersion,
					UID:     "test-uid",
					Allowed: false,
					Code:    403,
					Message: "Access denied",
					Reason:  "PolicyDenied",
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			},
			expectedResult: &Response{
				Version: APIVersion,
				UID:     "test-uid",
				Allowed: false,
				Code:    403,
				Message: "Access denied",
				Reason:  "PolicyDenied",
			},
		},
		{
			name: "server 500 error",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal server error"))
			},
			expectError: true,
			errorType:   &NetworkError{},
		},
		{
			name: "server 503 error",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte("service unavailable"))
			},
			expectError: true,
			errorType:   &NetworkError{},
		},
		{
			name: "invalid JSON response",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte("not json"))
			},
			expectError: true,
			errorType:   &InvalidResponseError{},
		},
		{
			name: "non-200 non-5xx response",
			serverHandler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte("bad request"))
			},
			expectError: true,
			errorType:   &InvalidResponseError{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.serverHandler)
			defer server.Close()

			cfg := Config{
				Name:          "test-webhook",
				URL:           server.URL,
				Timeout:       5 * time.Second,
				FailurePolicy: FailurePolicyFail,
			}

			client := newTestClient(cfg, TypeValidating, nil)

			req := &Request{
				Version:   APIVersion,
				UID:       "test-uid",
				Timestamp: time.Now(),
				Principal: &Principal{Sub: "user1"},
				Context: &RequestContext{
					ServerName: "test-server",
					SourceIP:   "127.0.0.1",
					Transport:  "sse",
				},
			}

			resp, err := client.Call(context.Background(), req)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, resp)
				if tt.errorType != nil {
					assert.True(t, errors.As(err, &tt.errorType),
						"expected error type %T, got %T", tt.errorType, err)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, resp)
				assert.Equal(t, tt.expectedResult.Version, resp.Version)
				assert.Equal(t, tt.expectedResult.UID, resp.UID)
				assert.Equal(t, tt.expectedResult.Allowed, resp.Allowed)
				assert.Equal(t, tt.expectedResult.Code, resp.Code)
				assert.Equal(t, tt.expectedResult.Message, resp.Message)
				assert.Equal(t, tt.expectedResult.Reason, resp.Reason)
			}
		})
	}
}

func TestClientCallMutating(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := MutatingResponse{
			Response: Response{
				Version: APIVersion,
				UID:     "test-uid",
				Allowed: true,
			},
			PatchType: "json_patch",
			Patch:     json.RawMessage(`[{"op":"add","path":"/mcp_request/params/arguments/audit","value":"true"}]`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		Name:          "test-mutating",
		URL:           server.URL,
		Timeout:       5 * time.Second,
		FailurePolicy: FailurePolicyIgnore,
	}

	client := newTestClient(cfg, TypeMutating, nil)

	req := &Request{
		Version:   APIVersion,
		UID:       "test-uid",
		Timestamp: time.Now(),
		Principal: &Principal{Sub: "user1"},
		Context: &RequestContext{
			ServerName: "test-server",
			SourceIP:   "127.0.0.1",
			Transport:  "sse",
		},
	}

	resp, err := client.CallMutating(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.True(t, resp.Allowed)
	assert.Equal(t, "json_patch", resp.PatchType)
	assert.NotEmpty(t, resp.Patch)
}

func TestClientHMACSigningHeaders(t *testing.T) {
	t.Parallel()

	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		resp := Response{
			Version: APIVersion,
			UID:     "test-uid",
			Allowed: true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		Name:          "test-hmac",
		URL:           server.URL,
		Timeout:       5 * time.Second,
		FailurePolicy: FailurePolicyFail,
	}
	hmacSecret := []byte("test-secret-key")

	client := newTestClient(cfg, TypeValidating, hmacSecret)

	req := &Request{
		Version:   APIVersion,
		UID:       "test-uid",
		Timestamp: time.Now(),
		Principal: &Principal{Sub: "user1"},
		Context: &RequestContext{
			ServerName: "test-server",
			SourceIP:   "127.0.0.1",
			Transport:  "sse",
		},
	}

	resp, err := client.Call(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify HMAC headers were sent.
	assert.NotEmpty(t, capturedHeaders.Get(SignatureHeader), "expected %s header", SignatureHeader)
	assert.Contains(t, capturedHeaders.Get(SignatureHeader), "sha256=")
	assert.NotEmpty(t, capturedHeaders.Get(TimestampHeader), "expected %s header", TimestampHeader)
}

func TestClientNoHMACHeadersWithoutSecret(t *testing.T) {
	t.Parallel()

	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeaders = r.Header
		resp := Response{
			Version: APIVersion,
			UID:     "test-uid",
			Allowed: true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		Name:          "test-no-hmac",
		URL:           server.URL,
		Timeout:       5 * time.Second,
		FailurePolicy: FailurePolicyFail,
	}

	client := newTestClient(cfg, TypeValidating, nil)

	req := &Request{
		Version:   APIVersion,
		UID:       "test-uid",
		Timestamp: time.Now(),
		Principal: &Principal{Sub: "user1"},
		Context: &RequestContext{
			ServerName: "test-server",
			SourceIP:   "127.0.0.1",
			Transport:  "sse",
		},
	}

	resp, err := client.Call(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify HMAC headers were NOT sent.
	assert.Empty(t, capturedHeaders.Get(SignatureHeader))
	assert.Empty(t, capturedHeaders.Get(TimestampHeader))
}

func TestClientTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Sleep longer than the client timeout.
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{
		Name:          "test-timeout",
		URL:           server.URL,
		Timeout:       100 * time.Millisecond,
		FailurePolicy: FailurePolicyFail,
	}

	client := newTestClient(cfg, TypeValidating, nil)

	req := &Request{
		Version:   APIVersion,
		UID:       "test-uid",
		Timestamp: time.Now(),
	}

	_, err := client.Call(context.Background(), req)
	require.Error(t, err)

	var timeoutErr *TimeoutError
	assert.True(t, errors.As(err, &timeoutErr), "expected TimeoutError, got %T: %v", err, err)
}

func TestClientResponseSizeLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Write more than MaxResponseSize bytes.
		largeBody := strings.Repeat("x", MaxResponseSize+100)
		w.Write([]byte(largeBody))
	}))
	defer server.Close()

	cfg := Config{
		Name:          "test-size-limit",
		URL:           server.URL,
		Timeout:       5 * time.Second,
		FailurePolicy: FailurePolicyFail,
	}

	client := newTestClient(cfg, TypeValidating, nil)

	req := &Request{
		Version:   APIVersion,
		UID:       "test-uid",
		Timestamp: time.Now(),
	}

	_, err := client.Call(context.Background(), req)
	require.Error(t, err)

	var invalidErr *InvalidResponseError
	assert.True(t, errors.As(err, &invalidErr), "expected InvalidResponseError, got %T: %v", err, err)
	assert.Contains(t, err.Error(), "exceeds maximum size")
}

func TestClientRequestContentType(t *testing.T) {
	t.Parallel()

	var capturedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedContentType = r.Header.Get("Content-Type")
		// Verify request body is valid JSON.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req Request
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := Response{
			Version: APIVersion,
			UID:     req.UID,
			Allowed: true,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := Config{
		Name:          "test-content-type",
		URL:           server.URL,
		Timeout:       5 * time.Second,
		FailurePolicy: FailurePolicyFail,
	}

	client := newTestClient(cfg, TypeValidating, nil)

	req := &Request{
		Version:   APIVersion,
		UID:       "test-uid",
		Timestamp: time.Now(),
		Principal: &Principal{Sub: "user1"},
		Context: &RequestContext{
			ServerName: "test-server",
			SourceIP:   "127.0.0.1",
			Transport:  "sse",
		},
	}

	resp, err := client.Call(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, "application/json", capturedContentType)
}

// newTestClient creates a webhook Client suitable for testing with httptest servers.
// It bypasses URL validation (httptest uses HTTP, not HTTPS).
func newTestClient(cfg Config, webhookType Type, hmacSecret []byte) *Client {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	return &Client{
		httpClient: &http.Client{
			Timeout: timeout,
		},
		config:      cfg,
		hmacSecret:  hmacSecret,
		webhookType: webhookType,
	}
}

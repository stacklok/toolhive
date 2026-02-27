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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/networking"
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

func TestBuildTransport(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	caFile := filepath.Join(tmpDir, "ca.crt")
	err := os.WriteFile(caFile, []byte("invalid-ca"), 0600)
	require.NoError(t, err)

	certFile := filepath.Join(tmpDir, "client.crt")
	keyFile := filepath.Join(tmpDir, "client.key")
	err = os.WriteFile(certFile, []byte("invalid-cert"), 0600)
	require.NoError(t, err)
	err = os.WriteFile(keyFile, []byte("invalid-key"), 0600)
	require.NoError(t, err)

	tests := []struct {
		name        string
		tlsCfg      *TLSConfig
		expectError bool
	}{
		{
			name:        "nil config",
			tlsCfg:      nil,
			expectError: false,
		},
		{
			name: "insecure skip verify",
			tlsCfg: &TLSConfig{
				InsecureSkipVerify: true,
			},
			expectError: false,
		},
		{
			name: "non-existent ca bundle",
			tlsCfg: &TLSConfig{
				CABundlePath: "/non/existent/path",
			},
			expectError: true,
		},
		{
			name: "invalid ca bundle content",
			tlsCfg: &TLSConfig{
				CABundlePath: caFile,
			},
			expectError: true,
		},
		{
			name: "non-existent client cert",
			tlsCfg: &TLSConfig{
				ClientCertPath: "/non/existent/cert",
				ClientKeyPath:  keyFile,
			},
			expectError: true,
		},
		{
			name: "invalid client cert/key",
			tlsCfg: &TLSConfig{
				ClientCertPath: certFile,
				ClientKeyPath:  keyFile,
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			transport, err := buildTransport(tt.tlsCfg)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, transport)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, transport)
				if tt.tlsCfg != nil && tt.tlsCfg.InsecureSkipVerify {
					vt, ok := transport.(*networking.ValidatingTransport)
					require.True(t, ok, "expected *networking.ValidatingTransport")
					tr, ok := vt.Transport.(*http.Transport)
					require.True(t, ok, "expected *http.Transport")
					assert.True(t, tr.TLSClientConfig.InsecureSkipVerify)
				}
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	t.Parallel()

	t.Run("non-timeout network error", func(t *testing.T) {
		t.Parallel()
		err := errors.New("connection refused")
		classified := classifyError("test", err)
		var netErr *NetworkError
		assert.True(t, errors.As(classified, &netErr))
	})
}

func TestTruncateBody(t *testing.T) {
	t.Parallel()

	t.Run("short body", func(t *testing.T) {
		t.Parallel()
		body := []byte("short")
		assert.Equal(t, "short", truncateBody(body))
	})

	t.Run("long body", func(t *testing.T) {
		t.Parallel()
		body := []byte(strings.Repeat("a", 300))
		truncated := truncateBody(body)
		assert.Equal(t, 256+3, len(truncated))
		assert.True(t, strings.HasSuffix(truncated, "..."))
	})
}

func TestClientCallErrors(t *testing.T) {
	t.Parallel()

	client := newTestClient(Config{
		Name:    "error-test",
		URL:     "invalid URL \x00", // Will cause http.NewRequest to fail
		Timeout: 1 * time.Second,
	}, TypeValidating, nil)

	t.Run("request creation failure", func(t *testing.T) {
		t.Parallel()
		_, err := client.Call(context.Background(), &Request{})
		assert.Error(t, err)
		var networkErr *NetworkError
		assert.True(t, errors.As(err, &networkErr))
	})

	t.Run("unmarshal failure Call", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not-json"))
		}))
		defer server.Close()

		testClient := newTestClient(Config{
			Name:          "unmarshal-fail",
			URL:           server.URL,
			FailurePolicy: FailurePolicyFail,
		}, TypeValidating, nil)

		_, err := testClient.Call(context.Background(), &Request{})
		assert.Error(t, err)
		var invalidErr *InvalidResponseError
		assert.True(t, errors.As(err, &invalidErr))
	})

	t.Run("unmarshal failure CallMutating", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("not-json"))
		}))
		defer server.Close()

		testClient := newTestClient(Config{
			Name:          "unmarshal-fail-mutating",
			URL:           server.URL,
			FailurePolicy: FailurePolicyFail,
		}, TypeMutating, nil)

		_, err := testClient.CallMutating(context.Background(), &Request{})
		assert.Error(t, err)
		var invalidErr *InvalidResponseError
		assert.True(t, errors.As(err, &invalidErr))
	})

	t.Run("doHTTPCall failure CallMutating", func(t *testing.T) {
		t.Parallel()
		testClient := newTestClient(Config{
			Name:          "http-fail",
			URL:           "http://invalid-address.local",
			FailurePolicy: FailurePolicyFail,
		}, TypeMutating, nil)
		_, err := testClient.CallMutating(context.Background(), &Request{})
		assert.Error(t, err)
	})
}

type errorReader struct{}

func (*errorReader) Read(_ []byte) (n int, err error) {
	return 0, errors.New("forced read error")
}
func (*errorReader) Close() error { return nil }

func TestDoHTTPCallReadError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// This handler won't be reached because we're testing doHTTPCall error path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// We need a server that returns a body that fails on Read
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		// We can't easily force Read to fail from net/http handler,
		// but we can mock the http client or its transport.
	}))
	defer ts.Close()

	cfg := Config{
		Name:          "read-err",
		URL:           ts.URL,
		FailurePolicy: FailurePolicyFail,
	}
	client, err := NewClient(cfg, TypeValidating, nil)
	require.NoError(t, err)

	// Mock the RoundTripper to return a body that fails on Read
	rt := &mockRoundTripper{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errorReader{},
		},
	}
	client.httpClient.Transport = rt

	_, err = client.Call(context.Background(), &Request{})
	assert.Error(t, err)
	var networkErr *NetworkError
	assert.True(t, errors.As(err, &networkErr))
	assert.Contains(t, err.Error(), "forced read error")
}

type mockRoundTripper struct {
	resp *http.Response
	err  error
}

func (m *mockRoundTripper) RoundTrip(_ *http.Request) (*http.Response, error) {
	return m.resp, m.err
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

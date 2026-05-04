// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validating

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/webhook"
)

// closedServerURL is a URL that will always fail to connect (port 0 is reserved/closed).
const closedServerURL = "http://127.0.0.1:0"

//nolint:paralleltest // Shares a mock HTTP server and lastRequest state
func TestValidatingMiddleware(t *testing.T) {
	// Setup a mock webhook server
	var lastRequest webhook.Request
	mockResponse := webhook.Response{
		Version: webhook.APIVersion,
		UID:     "resp-uid",
		Allowed: true,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		err := json.NewDecoder(r.Body).Decode(&lastRequest)
		require.NoError(t, err)

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(mockResponse)
		require.NoError(t, err)
	}))
	defer server.Close()

	// Create middleware handler
	config := []webhook.Config{
		{
			Name:          "test-webhook",
			URL:           server.URL,
			Timeout:       webhook.DefaultTimeout,
			FailurePolicy: webhook.FailurePolicyFail,
			TLSConfig: &webhook.TLSConfig{
				InsecureSkipVerify: true, // Need this for httptest server
			},
		},
	}

	var executors []clientExecutor
	for _, cfg := range config {
		client, err := webhook.NewClient(cfg, webhook.TypeValidating, nil)
		require.NoError(t, err)
		executors = append(executors, clientExecutor{client: client, config: cfg})
	}

	mw := createValidatingHandler(executors, "test-server", "stdio")

	t.Run("Allowed Request", func(t *testing.T) {
		mockResponse.Allowed = true // Server will return allowed

		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))

		// Add parsed MCP request and auth identity to context
		parsedMCP := &mcp.ParsedMCPRequest{
			Method: "tools/call",
			ID:     1,
		}
		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, parsedMCP)

		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: "user-1",
				Email:   "user@example.com",
				Groups:  []string{"admin"},
			},
		}
		ctx = auth.WithIdentity(ctx, identity)

		req = req.WithContext(ctx)
		req.RemoteAddr = "192.168.1.1:1234"

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, req)

		assert.True(t, nextCalled, "Next handler should be called for allowed request")
		assert.Equal(t, http.StatusOK, rr.Code)

		// Verify the payload sent to webhook
		assert.Equal(t, webhook.APIVersion, lastRequest.Version)
		assert.NotEmpty(t, lastRequest.UID)
		assert.NotZero(t, lastRequest.Timestamp)
		assert.JSONEq(t, string(reqBody), string(lastRequest.MCPRequest))

		require.NotNil(t, lastRequest.Context)
		assert.Equal(t, "test-server", lastRequest.Context.ServerName)
		assert.Equal(t, "stdio", lastRequest.Context.Transport)
		assert.Equal(t, "192.168.1.1:1234", lastRequest.Context.SourceIP)

		require.NotNil(t, lastRequest.Principal)
		assert.Equal(t, "user-1", lastRequest.Principal.Subject)
		assert.Equal(t, "user@example.com", lastRequest.Principal.Email)
		assert.Equal(t, []string{"admin"}, lastRequest.Principal.Groups)
	})

	t.Run("Allowed Request - No Identity", func(t *testing.T) {
		mockResponse.Allowed = true

		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{})
		req = req.WithContext(ctx)

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, req)

		assert.True(t, nextCalled)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Nil(t, lastRequest.Principal, "Principal should be nil")
	})

	t.Run("Denied Request", func(t *testing.T) {
		mockResponse.Allowed = false
		mockResponse.Message = "Custom deny message"
		mockResponse.Code = http.StatusForbidden

		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))

		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{})
		req = req.WithContext(ctx)

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, req)

		assert.False(t, nextCalled, "Next handler should not be called for denied request")
		assert.Equal(t, http.StatusForbidden, rr.Code)

		// The error response is a JSON-RPC format
		var errResp map[string]interface{}
		err := json.Unmarshal(rr.Body.Bytes(), &errResp)
		require.NoError(t, err)

		errObj, ok := errResp["Error"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, float64(http.StatusForbidden), errObj["code"])
		assert.Equal(t, "Request denied by policy", errObj["message"])
	})

	t.Run("Denied Request - Ignores Webhook Code Field", func(t *testing.T) {
		mockResponse.Allowed = false
		mockResponse.Message = "blocked"
		mockResponse.Code = 200 // out-of-range (not 4xx-5xx) should default to 403

		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{})
		req = req.WithContext(ctx)

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, req)

		assert.False(t, nextCalled)
		assert.Equal(t, http.StatusForbidden, rr.Code, "Webhook code should be ignored and default to 403")
	})

	t.Run("Webhook Error - Fail Policy", func(t *testing.T) {
		// Create a client pointing to a closed port to generate connection error
		cfg := config[0]
		cfg.URL = closedServerURL
		cfg.FailurePolicy = webhook.FailurePolicyFail

		failClient, err := webhook.NewClient(cfg, webhook.TypeValidating, nil)
		require.NoError(t, err)

		failMw := createValidatingHandler([]clientExecutor{{client: failClient, config: cfg}}, "test", "stdio")

		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{})
		req = req.WithContext(ctx)

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		failMw(nextHandler).ServeHTTP(rr, req)

		assert.False(t, nextCalled, "Next handler should not be called on evaluation error with fail policy")
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})

	t.Run("Webhook Error - Ignore Policy", func(t *testing.T) {
		// Create a client pointing to a closed port to generate connection error
		cfg := config[0]
		cfg.URL = closedServerURL
		cfg.FailurePolicy = webhook.FailurePolicyIgnore

		ignoreClient, err := webhook.NewClient(cfg, webhook.TypeValidating, nil)
		require.NoError(t, err)

		ignoreMw := createValidatingHandler([]clientExecutor{{client: ignoreClient, config: cfg}}, "test", "stdio")

		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{})
		req = req.WithContext(ctx)

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		ignoreMw(nextHandler).ServeHTTP(rr, req)

		assert.True(t, nextCalled, "Next handler should be called on evaluation error with ignore policy")
		assert.Equal(t, http.StatusOK, rr.Code)
	})

	t.Run("Skip Non-MCP Requests", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		// Missing parsed MCP request in context

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
		})

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, req)

		assert.True(t, nextCalled, "Next handler should be called for non-MCP requests")
		assert.Equal(t, http.StatusOK, rr.Code)
	})
}

func TestMiddlewareParams_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		params  MiddlewareParams
		wantErr bool
	}{
		{
			name:    "valid",
			params:  MiddlewareParams{Webhooks: []webhook.Config{{Name: "a", URL: "https://a", Timeout: webhook.DefaultTimeout, FailurePolicy: webhook.FailurePolicyFail}}},
			wantErr: false,
		},
		{
			name:    "empty webhooks",
			params:  MiddlewareParams{},
			wantErr: true,
		},
		{
			name:    "invalid webhook config",
			params:  MiddlewareParams{Webhooks: []webhook.Config{{Name: ""}}}, // Missing name
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.params.Validate()
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

type mockRunner struct {
	types.MiddlewareRunner
	middlewares map[string]types.Middleware
}

func (m *mockRunner) AddMiddleware(name string, mw types.Middleware) {
	if m.middlewares == nil {
		m.middlewares = make(map[string]types.Middleware)
	}
	m.middlewares[name] = mw
}

func TestCreateMiddleware(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{}

	// Create valid config JSON
	params := FactoryMiddlewareParams{
		MiddlewareParams: MiddlewareParams{
			Webhooks: []webhook.Config{
				{
					Name:          "test",
					URL:           "https://test.com/hook",
					Timeout:       webhook.DefaultTimeout,
					FailurePolicy: webhook.FailurePolicyIgnore,
				},
			},
		},
		ServerName: "test-server",
		Transport:  "stdio",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	mwConfig := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: paramsJSON,
	}

	err = CreateMiddleware(mwConfig, runner)
	require.NoError(t, err)

	require.Contains(t, runner.middlewares, MiddlewareType)
	mw := runner.middlewares[MiddlewareType]

	// Test Handler/Close methods to get 100% coverage
	require.NotNil(t, mw.Handler())
	require.NoError(t, mw.Close())
}

func TestCreateMiddleware_ResolvesHMACSecret(t *testing.T) {
	t.Setenv("TOOLHIVE_SECRETS_PROVIDER", "environment")
	t.Setenv("TOOLHIVE_SECRET_WEBHOOK_VALIDATING_TEST_HMAC_SECRET", "top-secret")

	var signatureHeader string
	var timestampHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signatureHeader = r.Header.Get(webhook.SignatureHeader)
		timestampHeader = r.Header.Get(webhook.TimestampHeader)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(webhook.Response{
			Version: webhook.APIVersion,
			UID:     "uid",
			Allowed: true,
		})
	}))
	defer server.Close()

	runner := &mockRunner{}
	params := FactoryMiddlewareParams{
		MiddlewareParams: MiddlewareParams{
			Webhooks: []webhook.Config{{
				Name:          "test",
				URL:           server.URL,
				Timeout:       webhook.DefaultTimeout,
				FailurePolicy: webhook.FailurePolicyIgnore,
				HMACSecretRef: "WEBHOOK_VALIDATING_TEST_HMAC_SECRET",
				TLSConfig: &webhook.TLSConfig{
					InsecureSkipVerify: true,
				},
			}},
		},
		ServerName: "test-server",
		Transport:  "stdio",
	}
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	err = CreateMiddleware(&types.MiddlewareConfig{Type: MiddlewareType, Parameters: paramsJSON}, runner)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)))
	req = req.WithContext(context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{
		Method: "tools/call",
		ID:     1,
	}))

	rr := httptest.NewRecorder()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	runner.middlewares[MiddlewareType].Handler()(next).ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotEmpty(t, signatureHeader)
	assert.NotEmpty(t, timestampHeader)
}

//nolint:paralleltest // Uses httptest server.
func TestValidatingMiddleware_HTTP422AlwaysDenies(t *testing.T) {
	tests := []struct {
		name          string
		failurePolicy webhook.FailurePolicy
	}{
		{
			name:          "fail policy",
			failurePolicy: webhook.FailurePolicyFail,
		},
		{
			name:          "ignore policy",
			failurePolicy: webhook.FailurePolicyIgnore,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte("unprocessable request"))
			}))
			defer server.Close()

			cfg := webhook.Config{
				Name:          "test-webhook",
				URL:           server.URL,
				Timeout:       webhook.DefaultTimeout,
				FailurePolicy: tt.failurePolicy,
				TLSConfig: &webhook.TLSConfig{
					InsecureSkipVerify: true,
				},
			}

			client, err := webhook.NewClient(cfg, webhook.TypeValidating, nil)
			require.NoError(t, err)

			mw := createValidatingHandler([]clientExecutor{{client: client, config: cfg}}, "test-server", "stdio")

			reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
			ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{Method: "tools/call", ID: 1})
			req = req.WithContext(ctx)

			var nextCalled bool
			nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				nextCalled = true
			})

			rr := httptest.NewRecorder()
			mw(nextHandler).ServeHTTP(rr, req)

			assert.False(t, nextCalled)
			assert.Equal(t, http.StatusForbidden, rr.Code)
			assert.Contains(t, rr.Body.String(), "Request denied by policy")
		})
	}
}

//nolint:paralleltest // Shares a mock HTTP server and lastRequest state
func TestMultiWebhookChain(t *testing.T) {
	// Setup mock webhook servers
	var lastRequest1, lastRequest2 webhook.Request
	mockResponse1 := webhook.Response{Version: webhook.APIVersion, UID: "resp-uid-1", Allowed: true}
	mockResponse2 := webhook.Response{Version: webhook.APIVersion, UID: "resp-uid-2", Allowed: true}

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&lastRequest1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResponse1)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&lastRequest2)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResponse2)
	}))
	defer server2.Close()

	// Create middleware handler with two webhooks
	config := []webhook.Config{
		{
			Name:          "hook-1",
			URL:           server1.URL,
			Timeout:       webhook.DefaultTimeout,
			FailurePolicy: webhook.FailurePolicyFail,
			TLSConfig:     &webhook.TLSConfig{InsecureSkipVerify: true},
		},
		{
			Name:          "hook-2",
			URL:           server2.URL,
			Timeout:       webhook.DefaultTimeout,
			FailurePolicy: webhook.FailurePolicyFail,
			TLSConfig:     &webhook.TLSConfig{InsecureSkipVerify: true},
		},
	}

	var executors []clientExecutor
	for _, cfg := range config {
		client, err := webhook.NewClient(cfg, webhook.TypeValidating, nil)
		require.NoError(t, err)
		executors = append(executors, clientExecutor{client: client, config: cfg})
	}
	mw := createValidatingHandler(executors, "test-server", "stdio")

	createReq := func() *http.Request {
		reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1}`)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
		ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, &mcp.ParsedMCPRequest{})
		return req.WithContext(ctx)
	}

	t.Run("Both Allow", func(t *testing.T) {
		mockResponse1.Allowed = true
		mockResponse2.Allowed = true
		lastRequest1 = webhook.Request{}
		lastRequest2 = webhook.Request{}

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, createReq())

		assert.True(t, nextCalled, "Next handler should be called when both webhooks allow")
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.NotEmpty(t, lastRequest1.UID, "First webhook should be called")
		assert.NotEmpty(t, lastRequest2.UID, "Second webhook should be called")
	})

	t.Run("First Denies, Second Skipped", func(t *testing.T) {
		mockResponse1.Allowed = false
		mockResponse1.Message = "Denied by hook-1"
		mockResponse2.Allowed = true // shouldn't matter
		lastRequest1 = webhook.Request{}
		lastRequest2 = webhook.Request{} // reset

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, createReq())

		assert.False(t, nextCalled, "Next handler should not be called")
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.NotEmpty(t, lastRequest1.UID, "First webhook should be called")
		assert.Empty(t, lastRequest2.UID, "Second webhook should NOT be called")

		// Verify error response
		var errResp map[string]interface{}
		_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
		errObj := errResp["Error"].(map[string]interface{})
		assert.Equal(t, "Request denied by policy", errObj["message"])
	})

	t.Run("First Allows, Second Denies", func(t *testing.T) {
		mockResponse1.Allowed = true
		mockResponse2.Allowed = false
		mockResponse2.Message = "Denied by hook-2"
		lastRequest1 = webhook.Request{}
		lastRequest2 = webhook.Request{}

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

		rr := httptest.NewRecorder()
		mw(nextHandler).ServeHTTP(rr, createReq())

		assert.False(t, nextCalled, "Next handler should not be called")
		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.NotEmpty(t, lastRequest1.UID, "First webhook should be called")
		assert.NotEmpty(t, lastRequest2.UID, "Second webhook should be called")

		// Verify error response
		var errResp map[string]interface{}
		_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
		errObj := errResp["Error"].(map[string]interface{})
		assert.Equal(t, "Request denied by policy", errObj["message"])
	})

	t.Run("Mixed Failure Policies: Err Ignore -> Allow", func(t *testing.T) {
		// Clone configs, set hook-1 to fail-open (ignore) and use bad URL
		cfg1 := config[0]
		cfg1.FailurePolicy = webhook.FailurePolicyIgnore
		cfg1.URL = closedServerURL // Force connection error
		client1, _ := webhook.NewClient(cfg1, webhook.TypeValidating, nil)

		cfg2 := config[1]
		client2, _ := webhook.NewClient(cfg2, webhook.TypeValidating, nil)

		mixedExecutors := []clientExecutor{
			{client: client1, config: cfg1},
			{client: client2, config: cfg2},
		}
		mixedMw := createValidatingHandler(mixedExecutors, "test-server", "stdio")

		mockResponse2.Allowed = true
		lastRequest2 = webhook.Request{}

		var nextCalled bool
		nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

		rr := httptest.NewRecorder()
		mixedMw(nextHandler).ServeHTTP(rr, createReq())

		assert.True(t, nextCalled, "Next handler should be called because error on first is ignored, and second allows")
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.NotEmpty(t, lastRequest2.UID, "Second webhook should be called")
	})
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mutating

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func makeConfig(url string, policy webhook.FailurePolicy) webhook.Config {
	return webhook.Config{
		Name:          "test-webhook",
		URL:           url,
		Timeout:       webhook.DefaultTimeout,
		FailurePolicy: policy,
		TLSConfig:     &webhook.TLSConfig{InsecureSkipVerify: true},
	}
}

func makeExecutors(t *testing.T, configs []webhook.Config) []clientExecutor {
	t.Helper()
	var executors []clientExecutor
	for _, cfg := range configs {
		client, err := webhook.NewClient(cfg, webhook.TypeMutating, nil)
		require.NoError(t, err)
		executors = append(executors, clientExecutor{client: client, config: cfg})
	}
	return executors
}

func makeMCPRequest(tb testing.TB, body []byte) *http.Request {
	tb.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	parsedMCP := &mcp.ParsedMCPRequest{
		Method: "tools/call",
		ID:     float64(1),
	}
	ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, parsedMCP)
	req = req.WithContext(ctx)
	req.RemoteAddr = "192.168.1.1:1234"
	return req
}

//nolint:paralleltest // Shares mock server state
func TestMutatingMiddleware_AllowedWithPatch(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"db","arguments":{"query":"SELECT *"}}}`

	// Build the mock webhook server that returns a patch adding "audit_user".
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req webhook.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		// Verify principal is forwarded.
		require.NotNil(t, req.Principal)
		assert.Equal(t, "user-1", req.Principal.Subject)

		patch := []JSONPatchOp{
			{Op: "add", Path: "/mcp_request/params/arguments/audit_user", Value: json.RawMessage(`"user@example.com"`)},
		}
		patchJSON, _ := json.Marshal(patch)

		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: req.UID, Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{makeConfig(server.URL, webhook.FailurePolicyFail)}), "srv", "stdio")

	req := makeMCPRequest(t, []byte(reqBody))
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-1", Email: "user@example.com"}}
	req = req.WithContext(auth.WithIdentity(req.Context(), identity))

	var capturedBody []byte
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
	})

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedBody)

	// Verify the mutated body has the new field.
	var mutated map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &mutated))
	params := mutated["params"].(map[string]interface{})
	args := params["arguments"].(map[string]interface{})
	assert.Equal(t, "user@example.com", args["audit_user"])
	assert.Equal(t, "SELECT *", args["query"])
}

//nolint:paralleltest
func TestMutatingMiddleware_AllowedNoPatch(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","method":"tools/call","id":1}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := webhook.MutatingResponse{
			Response: webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			// No patch
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{makeConfig(server.URL, webhook.FailurePolicyFail)}), "srv", "stdio")

	var nextCalled bool
	var capturedBody []byte
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedBody, _ = io.ReadAll(r.Body)
	})

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rr.Code)
	// Body should equal original since no patch was applied.
	assert.JSONEq(t, reqBody, string(capturedBody))
}

//nolint:paralleltest
func TestMutatingMiddleware_AllowedFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := webhook.MutatingResponse{
			Response: webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: false},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Request denied by webhook policy")
}

func TestMutatingMiddleware_WebhookError_FailPolicy(t *testing.T) {
	t.Parallel()
	cfg := makeConfig(closedServerURL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestMutatingMiddleware_WebhookError_IgnorePolicy(t *testing.T) {
	t.Parallel()
	cfg := makeConfig(closedServerURL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	const reqBody = `{"jsonrpc":"2.0","method":"tools/call","id":1}`
	var nextCalled bool
	var capturedBody []byte
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedBody, _ = io.ReadAll(r.Body)
	})

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	assert.True(t, nextCalled, "next should be called; error ignored per fail-open policy")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.JSONEq(t, reqBody, string(capturedBody))
}

func TestMutatingMiddleware_ScopeViolation_FailPolicy(t *testing.T) {
	t.Parallel()
	// Webhook tries to patch /principal/email — security violation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []JSONPatchOp{
			{Op: "replace", Path: "/principal/email", Value: json.RawMessage(`"hacked@evil.com"`)},
		}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestMutatingMiddleware_ScopeViolation_IgnorePolicy(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []JSONPatchOp{
			{Op: "replace", Path: "/principal/email", Value: json.RawMessage(`"hacked@evil.com"`)},
		}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	const reqBody = `{"jsonrpc":"2.0","id":1}`
	cfg := makeConfig(server.URL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	// fail-open: scope violation ignored, original body forwarded
	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_ChainedMutations(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"db","arguments":{"query":"SELECT *"}}}`

	// First webhook: adds "user" field.
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req webhook.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		// Verify we received the original body.
		assert.JSONEq(t, reqBody, string(req.MCPRequest))

		patch := []JSONPatchOp{
			{Op: "add", Path: "/mcp_request/params/arguments/user", Value: json.RawMessage(`"alice"`)},
		}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: req.UID, Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server1.Close()

	// Second webhook: adds "dept" field. Receives the output of webhook 1.
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req webhook.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		// Verify "user" field from webhook 1 is present.
		var mcpBody map[string]interface{}
		require.NoError(t, json.Unmarshal(req.MCPRequest, &mcpBody))
		params := mcpBody["params"].(map[string]interface{})
		args := params["arguments"].(map[string]interface{})
		assert.Equal(t, "alice", args["user"], "webhook 2 should receive output of webhook 1")

		patch := []JSONPatchOp{
			{Op: "add", Path: "/mcp_request/params/arguments/dept", Value: json.RawMessage(`"engineering"`)},
		}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: req.UID, Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server2.Close()

	cfg1 := makeConfig(server1.URL, webhook.FailurePolicyFail)
	cfg1.Name = "hook-1"
	cfg2 := makeConfig(server2.URL, webhook.FailurePolicyFail)
	cfg2.Name = "hook-2"

	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg1, cfg2}), "srv", "stdio")

	var capturedBody []byte
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
	})

	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	require.Equal(t, http.StatusOK, rr.Code)
	require.NotNil(t, capturedBody)

	var finalBody map[string]interface{}
	require.NoError(t, json.Unmarshal(capturedBody, &finalBody))
	params := finalBody["params"].(map[string]interface{})
	args := params["arguments"].(map[string]interface{})
	assert.Equal(t, "alice", args["user"], "user from webhook 1 should be present")
	assert.Equal(t, "engineering", args["dept"], "dept from webhook 2 should be present")
	assert.Equal(t, "SELECT *", args["query"], "original query should be preserved")
}

func TestMutatingMiddleware_SkipNonMCPRequests(t *testing.T) {
	t.Parallel()
	mw := createMutatingHandler(nil, "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })

	// No parsedMCP in context.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, req)

	assert.True(t, nextCalled, "non-MCP requests should pass through")
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestMiddlewareParams_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		params  MiddlewareParams
		wantErr bool
	}{
		{
			name: "valid",
			params: MiddlewareParams{Webhooks: []webhook.Config{
				{Name: "a", URL: "https://a.com/hook", Timeout: webhook.DefaultTimeout, FailurePolicy: webhook.FailurePolicyIgnore},
			}},
			wantErr: false,
		},
		{
			name:    "empty webhooks",
			params:  MiddlewareParams{},
			wantErr: true,
		},
		{
			name:    "invalid webhook config",
			params:  MiddlewareParams{Webhooks: []webhook.Config{{Name: ""}}},
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

	params := FactoryMiddlewareParams{
		MiddlewareParams: MiddlewareParams{
			Webhooks: []webhook.Config{
				{
					Name:          "test",
					URL:           "https://test.example.com/hook",
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
	require.NotNil(t, mw.Handler())
	require.NoError(t, mw.Close())
}

func TestCreateMiddleware_InvalidParams(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{}
	mwConfig := &types.MiddlewareConfig{
		Type:       MiddlewareType,
		Parameters: []byte(`not-valid-json`),
	}
	err := CreateMiddleware(mwConfig, runner)
	require.Error(t, err)
}

func TestCreateMiddleware_ValidationError(t *testing.T) {
	t.Parallel()
	runner := &mockRunner{}
	// Empty webhooks fails validation.
	params := FactoryMiddlewareParams{
		MiddlewareParams: MiddlewareParams{Webhooks: []webhook.Config{}},
		ServerName:       "srv",
		Transport:        "stdio",
	}
	paramsJSON, _ := json.Marshal(params)
	mwConfig := &types.MiddlewareConfig{Type: MiddlewareType, Parameters: paramsJSON}
	err := CreateMiddleware(mwConfig, runner)
	require.Error(t, err)
}

//nolint:paralleltest
func TestMutatingMiddleware_UnsupportedPatchType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: "strategic_merge", // unsupported type
			Patch:     json.RawMessage(`[{"op":"add","path":"/mcp_request/x","value":"y"}]`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// FailurePolicyFail → 500
	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_UnsupportedPatchType_IgnorePolicy(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","id":1}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: "strategic_merge",
			Patch:     json.RawMessage(`[{"op":"add","path":"/mcp_request/x","value":"y"}]`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// FailurePolicyIgnore: unsupported patch type is ignored, original body forwarded.
	cfg := makeConfig(server.URL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_MalformedPatchJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     json.RawMessage(`not-valid-json`),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_StringRequestID(t *testing.T) {
	// Tests that the middleware correctly handles a string JSON-RPC ID.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := webhook.MutatingResponse{
			Response: webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: false},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	reqBody := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":"string-id"}`)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBody))
	// Use string ID in parsedMCP.
	parsedMCP := &mcp.ParsedMCPRequest{Method: "tools/call", ID: "string-id"}
	ctx := context.WithValue(req.Context(), mcp.MCPRequestContextKey, parsedMCP)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "Request denied by webhook policy")

	// Confirm JSON-RPC error has the string ID.
	var errResp map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &errResp))
	require.NotNil(t, errResp["ID"])
}

//nolint:paralleltest
func TestMutatingMiddleware_InvalidPatchOp_FailPolicy(t *testing.T) {
	// Returns a well-formed JSON array but with an invalid op type, so
	// ValidatePatch returns an error inside the middleware handler.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// "delete" is not a valid RFC 6902 op, but the JSON is syntactically valid.
		patch := []map[string]interface{}{
			{"op": "delete", "path": "/mcp_request/params/key"},
		}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_InvalidPatchOp_IgnorePolicy(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","id":1}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []map[string]interface{}{
			{"op": "delete", "path": "/mcp_request/params/key"},
		}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestExtractMCPRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		wantBody string
	}{
		{
			name:     "valid envelope",
			input:    `{"mcp_request":{"jsonrpc":"2.0","id":1}}`,
			wantErr:  false,
			wantBody: `{"jsonrpc":"2.0","id":1}`,
		},
		{
			name:    "invalid JSON",
			input:   `{not-json`,
			wantErr: true,
		},
		{
			name:    "empty mcp_request field",
			input:   `{"other_field":"value"}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := extractMCPRequest([]byte(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantBody, string(result))
		})
	}
}

//nolint:paralleltest
func TestMutatingMiddleware_ApplyPatchFailure_FailPolicy(t *testing.T) {
	// Patch fails to apply because it removes a non-existent path
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []map[string]interface{}{{"op": "remove", "path": "/mcp_request/doesnotexist"}}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_ApplyPatchFailure_IgnorePolicy(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","id":1}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []map[string]interface{}{{"op": "remove", "path": "/mcp_request/doesnotexist"}}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_ExtractFailure_FailPolicy(t *testing.T) {
	// Patch removes /mcp_request, making extraction fail
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []map[string]interface{}{{"op": "remove", "path": "/mcp_request"}}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyFail)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { nextCalled = true })
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(`{"jsonrpc":"2.0","id":1}`)))

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

//nolint:paralleltest
func TestMutatingMiddleware_ExtractFailure_IgnorePolicy(t *testing.T) {
	const reqBody = `{"jsonrpc":"2.0","id":1}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		patch := []map[string]interface{}{{"op": "remove", "path": "/mcp_request"}}
		patchJSON, _ := json.Marshal(patch)
		resp := webhook.MutatingResponse{
			Response:  webhook.Response{Version: webhook.APIVersion, UID: "uid", Allowed: true},
			PatchType: patchTypeJSONPatch,
			Patch:     patchJSON,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	cfg := makeConfig(server.URL, webhook.FailurePolicyIgnore)
	mw := createMutatingHandler(makeExecutors(t, []webhook.Config{cfg}), "srv", "stdio")

	var nextCalled bool
	var capturedBody []byte
	nextHandler := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		capturedBody, _ = io.ReadAll(r.Body)
	})
	rr := httptest.NewRecorder()
	mw(nextHandler).ServeHTTP(rr, makeMCPRequest(t, []byte(reqBody)))

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.JSONEq(t, reqBody, string(capturedBody))
}

func TestValidatePatchErrors(t *testing.T) {
	t.Parallel()
	invalidOps := []JSONPatchOp{
		{Op: "copy", Path: "/mcp_request/a"}, // missing From
		{Op: "move", Path: "/mcp_request/b"}, // missing From
		{Op: "invalid_op", Path: "/mcp_request/c"},
		{Op: "add", Path: ""}, // missing Path
	}
	err := ValidatePatch(invalidOps)
	require.Error(t, err)
}

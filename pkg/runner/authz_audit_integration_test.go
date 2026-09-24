// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/webhook"
	statusesmocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

// buildRunnerMiddlewareChain populates the middleware configs for runConfig,
// instantiates the middlewares through the runner factories (the same path the
// proxyrunner takes), and wraps final with them in the order the proxies apply
// them (reverse slice order, so index 0 is the outermost wrapper).
func buildRunnerMiddlewareChain(t *testing.T, runConfig *RunConfig, final http.Handler) http.Handler {
	t.Helper()

	require.NoError(t, PopulateMiddlewareConfigs(runConfig))

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockStatusManager := statusesmocks.NewMockStatusManager(ctrl)

	runner := NewRunner(runConfig, mockStatusManager)
	for _, mwConfig := range runConfig.MiddlewareConfigs {
		factory, ok := runner.supportedMiddleware[mwConfig.Type]
		require.True(t, ok, "no factory for middleware type %q", mwConfig.Type)
		require.NoError(t, factory(&mwConfig, runner))
	}
	require.NotEmpty(t, runner.middlewares)
	// The factories acquire resources (the auditor's log file, the usage-metrics
	// flush goroutine); release them when the test finishes.
	t.Cleanup(func() {
		for _, mw := range runner.middlewares {
			_ = mw.Close()
		}
	})

	handler := final
	for i := len(runner.middlewares) - 1; i >= 0; i-- {
		handler = runner.middlewares[i].Handler()(handler)
	}
	return handler
}

// readAuditEvents parses the newline-delimited JSON audit log at path.
func readAuditEvents(t *testing.T, path string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event), "audit log line is not JSON: %s", line)
		events = append(events, event)
	}
	return events
}

// TestAuthzDecisionIsAudited proves, through the full middleware chain the
// proxyrunner builds via PopulateMiddlewareConfigs, that authorization
// decisions produce audit events: a Cedar-denied tools/call must return HTTP
// 403 AND still emit an audit event with outcome "denied", and an authorized
// call must emit one with outcome "success". This is the regression guard for
// the ordering bug where audit was wired inside authz and denials were never
// audited.
func TestAuthzDecisionIsAudited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		policy          string
		wantStatus      int
		wantOutcome     string
		wantHandlerHit  bool
		wantDeniedError bool
	}{
		{
			name: "denied tool call returns 403 and is audited with outcome denied",
			// Permit only an unrelated tool: "target_tool" is default-denied.
			policy:          `permit(principal, action == Action::"call_tool", resource == Tool::"some_other_tool");`,
			wantStatus:      http.StatusForbidden,
			wantOutcome:     "denied",
			wantHandlerHit:  false,
			wantDeniedError: true,
		},
		{
			name:           "allowed tool call returns 200 and is audited with outcome success",
			policy:         `permit(principal, action, resource);`,
			wantStatus:     http.StatusOK,
			wantOutcome:    "success",
			wantHandlerHit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authzConfig, err := authorizers.NewConfig(cedar.Config{
				Version: "1.0",
				Type:    cedar.ConfigType,
				Options: &cedar.ConfigOptions{
					Policies:     []string{tt.policy},
					EntitiesJSON: "[]",
				},
			})
			require.NoError(t, err)

			auditLogPath := filepath.Join(t.TempDir(), "audit.log")

			runConfig := NewRunConfig()
			runConfig.Name = "test-server"
			runConfig.AuthzConfig = authzConfig
			runConfig.AuditConfig = &audit.Config{
				Component: "test-component",
				LogFile:   auditLogPath,
			}

			handlerHit := false
			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerHit = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			})

			handler := buildRunnerMiddlewareChain(t, runConfig, backend)

			reqBody := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"target_tool","arguments":{}}}`
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			require.Equal(t, tt.wantStatus, rr.Code, "response body: %s", rr.Body.String())
			assert.Equal(t, tt.wantHandlerHit, handlerHit)
			if tt.wantDeniedError {
				assert.Contains(t, rr.Body.String(), "Unauthorized",
					"denied response must carry the authorization-denied error")
			}

			events := readAuditEvents(t, auditLogPath)
			require.Len(t, events, 1, "exactly one audit event must be emitted")
			event := events[0]
			assert.Equal(t, "mcp_tool_call", event["type"],
				"the parsed MCP method must drive the audit event type")
			assert.Equal(t, tt.wantOutcome, event["outcome"])
		})
	}
}

// TestTransparentProxyAuthorizesJSONPostsOnSSESuffixes verifies the middleware
// chain installed by the runner before a transparent proxy forwards traffic.
func TestTransparentProxyAuthorizesJSONPostsOnSSESuffixes(t *testing.T) {
	t.Parallel()

	auditLogPath := filepath.Join(t.TempDir(), "audit.log")
	authzConfig, err := authorizers.NewConfig(cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
			EntitiesJSON: "[]",
		},
	})
	require.NoError(t, err)

	runConfig := NewRunConfig()
	runConfig.Name = "test-server"
	runConfig.AuthzConfig = authzConfig
	runConfig.AuditConfig = &audit.Config{Component: "test-component", LogFile: auditLogPath}
	require.NoError(t, PopulateMiddlewareConfigs(runConfig))

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	runner := NewRunner(runConfig, statusesmocks.NewMockStatusManager(ctrl))
	for _, mwConfig := range runConfig.MiddlewareConfigs {
		factory, ok := runner.supportedMiddleware[mwConfig.Type]
		require.True(t, ok, "no factory for middleware type %q", mwConfig.Type)
		require.NoError(t, factory(&mwConfig, runner))
	}
	t.Cleanup(func() {
		for _, mw := range runner.middlewares {
			_ = mw.Close()
		}
	})

	var mu sync.Mutex
	var backendBodies []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		mu.Lock()
		backendBodies = append(backendBodies, string(body))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"tools/list"`) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":4,"result":{"tools":[{"name":"weather"},{"name":"admin_tool"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":5,"result":{"ok":true}}`))
	}))
	t.Cleanup(backend.Close)

	proxy := transparent.NewTransparentProxy(
		"127.0.0.1", 0, backend.URL, nil, nil, nil, false, false, "sse", nil, nil, "", false, runner.namedMiddlewares...,
	)
	proxyCtx, cancelProxy := context.WithCancel(t.Context())
	t.Cleanup(cancelProxy)
	require.NoError(t, proxy.Start(proxyCtx))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, proxy.Stop(ctx))
	})

	client := &http.Client{Timeout: 5 * time.Second}
	post := func(path, body string) *http.Response {
		t.Helper()
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+proxy.ListenerAddr()+path, strings.NewReader(body))
		require.NoError(t, reqErr)
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := client.Do(req)
		require.NoError(t, doErr)
		return resp
	}
	readBody := func(resp *http.Response) []byte {
		t.Helper()
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.NoError(t, resp.Body.Close())
		return body
	}

	deniedCall := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"admin_tool","arguments":{}}}`
	for _, path := range []string{"/mcp", "/sse", "/x/sse"} {
		resp := post(path, deniedCall)
		body := readBody(resp)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "path %s: %s", path, body)
		var denied struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int64  `json:"id"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &denied))
		assert.Equal(t, "2.0", denied.JSONRPC)
		assert.Equal(t, int64(1), denied.ID)
		assert.Equal(t, "Unauthorized", denied.Error.Message)
	}

	for _, path := range []string{"/mcp", "/sse", "/x/sse"} {
		listResponse := post(path, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`)
		listBody := readBody(listResponse)
		require.Equal(t, http.StatusOK, listResponse.StatusCode, "path %s: %s", path, listBody)

		var listed struct {
			ID     int64 `json:"id"`
			Result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal(listBody, &listed), "path %s returned invalid JSON: %s", path, listBody)
		assert.Equal(t, int64(4), listed.ID, "path %s must preserve the JSON-RPC response ID", path)
		require.Len(t, listed.Result.Tools, 1, "path %s must expose only authorized tools", path)
		assert.Equal(t, "weather", listed.Result.Tools[0].Name, "path %s must expose only weather", path)
	}

	allowedCall := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"weather","arguments":{"city":"Paris"}}}`
	allowedResponse := post("/x/sse", allowedCall)
	allowedBody := readBody(allowedResponse)
	require.Equal(t, http.StatusOK, allowedResponse.StatusCode, string(allowedBody))

	mu.Lock()
	assert.Equal(t, []string{
		`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`,
		allowedCall,
	}, backendBodies, "only authorized requests must reach the backend")
	mu.Unlock()

	events := readAuditEvents(t, auditLogPath)
	var deniedSuffixAudit map[string]any
	for _, event := range events {
		target, ok := event["target"].(map[string]any)
		if ok && target["endpoint"] == "/x/sse" && event["outcome"] == "denied" {
			deniedSuffixAudit = event
			break
		}
	}
	require.NotNil(t, deniedSuffixAudit, "the denied /x/sse request must produce an audit event")
	assert.Equal(t, "mcp_tool_call", deniedSuffixAudit["type"], "a denied suffix tools/call must be classified as an MCP tool call")
	assert.Equal(t, "denied", deniedSuffixAudit["outcome"], "the denied suffix call must be recorded as denied")
	assert.Equal(t, map[string]any{
		"endpoint": "/x/sse",
		"method":   "tools/call",
		"name":     "admin_tool",
		"type":     "tool",
	}, deniedSuffixAudit["target"], "the denied suffix event must identify the requested admin tool")
}

// TestWebhookDenialIsAudited proves, through the full middleware chain built
// by PopulateMiddlewareConfigs, that a validating-webhook policy denial (403)
// still produces an audit event with outcome "denied". Webhooks run inside
// the audit middleware, so the rejection must be captured like any other.
func TestWebhookDenialIsAudited(t *testing.T) {
	t.Parallel()

	denyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req webhook.Request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := webhook.Response{
			Version: webhook.APIVersion,
			UID:     req.UID,
			Allowed: false,
			Reason:  "policy denied",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(denyServer.Close)

	auditLogPath := filepath.Join(t.TempDir(), "audit.log")

	runConfig := NewRunConfig()
	runConfig.Name = "test-server"
	runConfig.ValidatingWebhooks = []webhook.Config{{
		Name:          "deny-all",
		URL:           denyServer.URL,
		Timeout:       webhook.DefaultTimeout,
		FailurePolicy: webhook.FailurePolicyFail,
		TLSConfig:     &webhook.TLSConfig{InsecureSkipVerify: true},
	}}
	runConfig.AuditConfig = &audit.Config{
		Component: "test-component",
		LogFile:   auditLogPath,
	}

	handlerHit := false
	backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerHit = true
		w.WriteHeader(http.StatusOK)
	})

	handler := buildRunnerMiddlewareChain(t, runConfig, backend)

	reqBody := `{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{"name":"target_tool","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "response body: %s", rr.Body.String())
	assert.False(t, handlerHit, "a webhook-denied request must not reach the backend")

	events := readAuditEvents(t, auditLogPath)
	require.Len(t, events, 1, "a webhook policy denial must produce exactly one audit event")
	assert.Equal(t, "mcp_tool_call", events[0]["type"])
	assert.Equal(t, "denied", events[0]["outcome"])
}

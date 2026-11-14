// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

const admissionPermitEcho = `permit(principal, action == Action::"call_tool", resource == Tool::"echo");`
const admissionForbidBlocked = `forbid(principal, action == Action::"call_tool", resource == Tool::"echo") when { context has arg_input && context.arg_input == "blocked" };`

func admissionCall(modern bool, input string) string {
	meta := ""
	if modern {
		meta = `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"input":%q}%s}}`, input, meta)
}

func postAdmission(t *testing.T, baseURL, sessionID, body, contentType string, modern bool) (int, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if modern {
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", "echo")
	} else {
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	result, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode, result
}

//nolint:paralleltest // Each mode's subtests share a session/backend and assert sequential dispatch counts.
func TestAdmissionVMCPRealHandlers(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name         string
		auth, modern bool
	}{
		{"nil_auth_modern", false, true}, {"nil_auth_legacy", false, false},
		{"cedar_modern", true, true}, {"cedar_legacy", true, false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			backendURL := startRealMCPBackendWithToolDelay(t, 0, func() { calls.Add(1) })
			var ts *httptest.Server
			if mode.auth {
				ts = buildCedarAuthzServer(t, backendURL, nil, nil, nil, admissionPermitEcho, admissionForbidBlocked)
			} else {
				ts = newRealTestServer(t, backendURL)
			}
			sessionID := ""
			if !mode.modern {
				client := NewMCPTestClient(t, ts.URL)
				sessionID = client.InitializeSession()
				waitForEchoTool(t, ts.URL, sessionID)
			}
			valid := admissionCall(mode.modern, "healthy")
			for _, tc := range []struct {
				name, body, contentType string
				code                    int
			}{
				{"malformed_plain", `{"jsonrpc":`, "text/plain", -32700},
				{"batch_missing", `[` + valid + `]`, "", -32600},
				{"duplicate_json", strings.Replace(valid, `"method":"tools/call"`, `"method":"ping","method":"tools/call"`, 1), "application/json", -32600},
				{"alias_plain", strings.Replace(valid, `"method":`, `"Method":`, 1), "text/plain", -32600},
				{"mixed_missing", strings.Replace(valid, `"id":7`, `"id":7,"result":{}`, 1), "", -32600},
				{"mixed_json", strings.Replace(valid, `"id":7`, `"id":7,"result":{}`, 1), "application/json", -32600},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := calls.Load()
					status, body := postAdmission(t, ts.URL, sessionID, tc.body, tc.contentType, mode.modern)
					assert.Equal(t, http.StatusBadRequest, status, "body: %s", body)
					rpcErr := parseRPCError(t, body)
					assert.True(t, rpcErr.present, "parser must reject before SDK media-type handling: %s", body)
					assert.Equal(t, tc.code, rpcErr.code)
					assert.Equal(t, before, calls.Load(), "invalid input must not dispatch to backend")
				})
			}
			for _, contentType := range []string{"application/json", "text/plain", ""} {
				t.Run("valid_"+contentType, func(t *testing.T) {
					before := calls.Load()
					status, body := postAdmission(t, ts.URL, sessionID, valid, contentType, mode.modern)
					if !mode.modern && contentType != "application/json" {
						// The legacy SDK enforces its own media-type contract after admission.
						assert.Equal(t, http.StatusUnsupportedMediaType, status, "body: %s", body)
						assert.Equal(t, before, calls.Load())
						return
					}
					require.Equal(t, http.StatusOK, status, "body: %s", body)
					assert.False(t, parseRPCError(t, body).present, "body: %s", body)
					assert.Contains(t, string(body), `"text":"healthy"`)
					assert.Equal(t, before+1, calls.Load(), "positive control must execute the tool")
					if mode.auth {
						status, body = postAdmission(t, ts.URL, sessionID, admissionCall(mode.modern, "blocked"), contentType, mode.modern)
						assert.Equal(t, http.StatusForbidden, status, "body: %s", body)
						assert.Equal(t, 403, parseRPCError(t, body).code)
						assert.Equal(t, before+1, calls.Load(), "Cedar-denied arguments must not execute")
					}
				})
			}
		})
	}
}

func TestAdmissionVMCPModernDomainRateLimit(t *testing.T) {
	t.Parallel()
	// Config.RateLimiter decorates core.CallTool; no direct-proxy HTTP limiter is installed.
	for _, tc := range []struct {
		name, contentType string
		auth              bool
	}{
		{"nil_auth_json", "application/json", false}, {"nil_auth_plain", "text/plain", false}, {"nil_auth_missing", "", false},
		{"auth_json", "application/json", true}, {"auth_plain", "text/plain", true}, {"auth_missing", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			limiter, err := ratelimit.NewLimiter(client, "test", "vmcp", &v1beta1.RateLimitConfig{
				Shared: &v1beta1.RateLimitBucket{MaxTokens: 1, RefillPeriod: metav1.Duration{Duration: time.Hour}},
			})
			require.NoError(t, err)
			configure := func(cfg *server.Config) { cfg.RateLimiter = limiter }
			var calls atomic.Int32
			backendURL := startRealMCPBackendWithToolDelay(t, 0, func() { calls.Add(1) })
			var ts *httptest.Server
			if tc.auth {
				ts = buildCedarAuthzServerWithConfig(t, backendURL, nil, nil, nil, configure, admissionPermitEcho)
			} else {
				ts = httptest.NewServer(newRealTestHandler(t, backendURL, configure))
				t.Cleanup(ts.Close)
			}
			valid := admissionCall(true, "metered")
			status, body := postAdmission(t, ts.URL, "", valid, tc.contentType, true)
			require.Equal(t, http.StatusOK, status, "body: %s", body)
			require.Contains(t, string(body), `"text":"metered"`)
			require.EqualValues(t, 1, calls.Load())
			// The non-JSON call must consume the same quota as a subsequent JSON call.
			status, body = postAdmission(t, ts.URL, "", valid, "application/json", true)
			assert.Equal(t, http.StatusOK, status, "Modern coded errors use HTTP 200")
			rpcErr := parseRPCError(t, body)
			require.True(t, rpcErr.present, "body: %s", body)
			assert.EqualValues(t, ratelimit.CodeRateLimited, rpcErr.code)
			assert.Equal(t, ratelimit.MessageRateLimited, rpcErr.message)
			assert.EqualValues(t, 1, calls.Load(), "rate-limited request must not dispatch")
		})
	}
}

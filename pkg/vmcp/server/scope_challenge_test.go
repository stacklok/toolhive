// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

func scopeChallengeFixture(mode string) *Config {
	return &Config{Name: "gateway", Version: "test",
		ScopeAuthorization: &vmcp.ScopeAuthorizationConfig{Tools: map[string]vmcp.OAuthScopeSet{"echo": {"write"}}},
		ScopeChallenge:     &ScopeChallengeConfig{ResourceURL: "https://gateway.example/mcp", ResponseMode: mode},
	}
}

func TestScopeChallengeProtocolPaths(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"http", "tool-result"} {
		for _, modern := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/modern=%v", mode, modern), func(t *testing.T) {
				t.Parallel()
				fc := &modernFakeCore{checkToolErr: fmt.Errorf("wrapped: %w", &vmcp.InsufficientScopeError{RequiredScopes: []string{"read", "write"}})}
				s := &Server{core: fc, config: scopeChallengeFixture(mode)}
				marker := &mcpparser.AuthzDenialMarker{}
				ctx := mcpparser.WithAuthzDenialMarker(t.Context(), marker)
				body := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"echo","arguments":{}}}`
				rec := httptest.NewRecorder()
				if modern {
					s.dispatchModernToolCall(ctx, rec, &mcpparser.ParsedMCPRequest{
						Method: "tools/call", ResourceID: "echo", ID: 42, Params: json.RawMessage(`{"name":"echo","arguments":{}}`),
					}, nil)
				} else {
					h := s.scopeChallengeMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
						t.Fatal("refused call reached the SDK")
					}))
					req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body)).WithContext(ctx)
					mcpparser.ParsingMiddleware(h).ServeHTTP(rec, req)
				}
				require.True(t, fc.checkCalled)
				require.False(t, fc.callCalled, "refusal must happen before execution")
				require.True(t, marker.Denied, "HTTP200 compatibility refusal must still audit as denied")
				require.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
				var envelope struct {
					ID     int             `json:"id"`
					Error  json.RawMessage `json:"error"`
					Result struct {
						IsError    bool                       `json:"isError"`
						ResultType string                     `json:"resultType"`
						Meta       map[string]json.RawMessage `json:"_meta"`
					} `json:"result"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
				require.Equal(t, 42, envelope.ID)
				challenge := rec.Header().Get("WWW-Authenticate")
				if mode == "http" {
					require.Equal(t, http.StatusForbidden, rec.Code)
					require.NotEmpty(t, envelope.Error)
				} else {
					require.Equal(t, http.StatusOK, rec.Code)
					require.Empty(t, challenge)
					require.True(t, envelope.Result.IsError)
					var challenges []string
					require.NoError(t, json.Unmarshal(envelope.Result.Meta["mcp/www_authenticate"], &challenges))
					require.Len(t, challenges, 1)
					challenge = challenges[0]
					if modern {
						require.Equal(t, "complete", envelope.Result.ResultType)
					}
				}
				require.Contains(t, challenge, `error="insufficient_scope"`)
				require.Contains(t, challenge, `scope="read write"`)
				require.Contains(t, challenge, `resource_metadata="https://gateway.example/.well-known/oauth-protected-resource/mcp"`)
			})
		}
	}
}

func TestScopeChallengeOrdinaryDenialAndPostCheck(t *testing.T) {
	t.Parallel()
	fc := &modernFakeCore{checkToolErr: vmcp.ErrAuthorizationFailed}
	s := &Server{core: fc, config: scopeChallengeFixture("tool-result")}
	parsed := &mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "echo", ID: 1, Params: json.RawMessage(`{"name":"echo"}`)}
	rec := httptest.NewRecorder()
	s.dispatchModernToolCall(t.Context(), rec, parsed, nil)
	require.Equal(t, http.StatusForbidden, rec.Code, "ordinary policy/malformed-claim denials must not become scope upgrades")
	require.Empty(t, rec.Header().Get("WWW-Authenticate"))
	require.NotContains(t, rec.Body.String(), "mcp/www_authenticate")
	require.False(t, fc.callCalled)
	fc.checkToolErr = nil
	fc.callToolErr = &vmcp.InsufficientScopeError{RequiredScopes: []string{"write"}}
	rec = httptest.NewRecorder()
	s.dispatchModernToolCall(t.Context(), rec, parsed, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "mcp/www_authenticate", "call-path refusals use the same encoding as preflight")
}

func TestScopeChallengeMetadataAndDiscovery(t *testing.T) {
	t.Parallel()
	for resource, expected := range map[string]string{
		"https://gateway.example/":     "https://gateway.example/.well-known/oauth-protected-resource",
		"https://gateway.example/mcp/": "https://gateway.example/.well-known/oauth-protected-resource/mcp/",
	} {
		got, err := scopeMetadataURL(resource)
		require.NoError(t, err)
		require.Equal(t, expected, got)
	}
	for _, invalid := range []string{"", "opaque-audience", "https://u:p@gateway.example", "https://gateway.example/#x", "https://gateway.example/?x=1"} {
		_, err := scopeMetadataURL(invalid)
		require.Error(t, err)
	}
	for _, mode := range []string{"http", "auto", "tool-result"} {
		s := &Server{config: scopeChallengeFixture(mode)}
		tool := mcp.Tool{Name: "echo", Meta: mcp.NewMetaFromMap(map[string]any{"existing": "preserved"})}
		s.annotateScopeTool(&tool)
		raw, err := json.Marshal(tool)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"securitySchemes"`, "mapped tool metadata must be independent of refusal encoding")
		require.Contains(t, string(raw), `"scopes":["write"]`)
		require.Contains(t, string(raw), `"existing":"preserved"`)
		unmapped := mcp.Tool{Name: "unmapped"}
		s.annotateScopeTool(&unmapped)
		require.Nil(t, unmapped.Meta)
	}
	require.NotPanics(t, func() { (&Server{}).annotateScopeTool(&mcp.Tool{}) })
}

func TestScopeChallengeLegacyOrdinaryDenial(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"http", "tool-result", "auto"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fc := &modernFakeCore{checkToolErr: fmt.Errorf("wrapped: %w", vmcp.ErrAuthorizationFailed)}
			s := &Server{core: fc, config: scopeChallengeFixture(mode)}
			nextCalled := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`))
			rec := httptest.NewRecorder()
			mcpparser.ParsingMiddleware(s.scopeChallengeMiddleware(next)).ServeHTTP(rec, req)

			require.Equal(t, http.StatusForbidden, rec.Code)
			require.Empty(t, rec.Header().Get("WWW-Authenticate"))
			require.NotContains(t, rec.Body.String(), "mcp/www_authenticate")
			require.True(t, fc.checkCalled)
			require.False(t, fc.callCalled)
			require.False(t, nextCalled, "ordinary denial must be rendered before SDK dispatch")
			var envelope struct {
				ID    int `json:"id"`
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
			require.Equal(t, 1, envelope.ID)
			require.Equal(t, http.StatusForbidden, envelope.Error.Code)
		})
	}
}

func TestScopeChallengeProtocolValidationPrecedesChallenge(t *testing.T) {
	t.Parallel()
	const toolCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`
	for _, tc := range []struct {
		name       string
		method     string
		body       string
		disabled   bool
		skipParser bool
		wantStatus int
		wantNext   bool
	}{
		{
			name: "disabled scope policy", method: http.MethodPost, body: toolCall,
			disabled: true, wantStatus: http.StatusAccepted, wantNext: true,
		},
		{
			name: "GET", method: http.MethodGet,
			wantStatus: http.StatusAccepted, wantNext: true,
		},
		{
			name: "tools/list", method: http.MethodPost,
			body:       `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
			wantStatus: http.StatusAccepted, wantNext: true,
		},
		{
			name: "no parsed request", method: http.MethodPost, body: toolCall,
			skipParser: true, wantStatus: http.StatusAccepted, wantNext: true,
		},
		{
			name: "notification without id", method: http.MethodPost,
			body:       `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"echo"}}`,
			wantStatus: http.StatusAccepted, wantNext: true,
		},
		{
			name: "non-object arguments", method: http.MethodPost,
			body:       `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":[]}}`,
			wantStatus: http.StatusAccepted, wantNext: true,
		},
		{
			name: "batch rejected by parser", method: http.MethodPost,
			body: "[" + toolCall + "]", wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc := &modernFakeCore{checkToolErr: &vmcp.InsufficientScopeError{RequiredScopes: []string{"write"}}}
			cfg := scopeChallengeFixture("auto")
			if tc.disabled {
				cfg.ScopeAuthorization = nil
			}
			s := &Server{core: fc, config: cfg}
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusAccepted)
			})
			var handler http.Handler = s.scopeChallengeMiddleware(next)
			if !tc.skipParser {
				handler = mcpparser.ParsingMiddleware(handler)
			}
			req := httptest.NewRequest(tc.method, "/mcp", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantNext, nextCalled)
			require.Empty(t, rec.Header().Get("WWW-Authenticate"))
			require.NotContains(t, rec.Body.String(), "mcp/www_authenticate")
			require.False(t, fc.checkCalled)
			require.False(t, fc.callCalled)
		})
	}
	t.Run("Modern non-object arguments", func(t *testing.T) {
		t.Parallel()
		fc := &modernFakeCore{checkToolErr: vmcp.ErrAuthorizationFailed}
		s := &Server{core: fc, config: scopeChallengeFixture("auto")}
		rec := httptest.NewRecorder()
		s.dispatchModernToolCall(t.Context(), rec, &mcpparser.ParsedMCPRequest{
			Method: "tools/call", ResourceID: "echo", ID: 1, Params: json.RawMessage(`{"name":"echo","arguments":[]}`),
		}, nil)
		require.False(t, fc.checkCalled)
		require.False(t, fc.callCalled)
		require.NotContains(t, rec.Body.String(), "mcp/www_authenticate")
	})
}

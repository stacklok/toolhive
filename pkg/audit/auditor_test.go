// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/mcp"
)

func TestNewAuditor(t *testing.T) {
	t.Parallel()
	config := &Config{}
	auditor, err := NewAuditorWithTransport(config, "sse")

	assert.NoError(t, err)
	assert.NotNil(t, auditor)
	assert.Equal(t, config, auditor.config)
}

func TestAuditorMiddlewareDisabled(t *testing.T) {
	t.Parallel()
	config := &Config{}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("test response"))
		require.NoError(t, err)
	})

	middleware := auditor.Middleware(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "test response", rr.Body.String())
}

func TestAuditorMiddlewareWithRequestData(t *testing.T) {
	t.Parallel()
	config := &Config{
		IncludeRequestData: true,
		MaxDataSize:        1024,
	}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the body to ensure it's still available
		body := make([]byte, 100)
		n, _ := r.Body.Read(body)
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(body[:n])
		require.NoError(t, err)
	})

	middleware := auditor.Middleware(handler)

	requestBody := `{"test": "data"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, requestBody, rr.Body.String())
}

func TestAuditorMiddlewareWithOversizedRequestData(t *testing.T) {
	t.Parallel()

	// Use a small MaxDataSize to easily create an "oversized" body
	maxSize := 10
	config := &Config{
		IncludeRequestData: true,
		MaxDataSize:        maxSize,
	}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	// Track whether the handler received the complete body
	var receivedBody string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})

	middleware := auditor.Middleware(handler)

	// Create a request body that exceeds MaxDataSize
	oversizedBody := "This is a body that exceeds the max data size limit"
	require.Greater(t, len(oversizedBody), maxSize, "Test body must exceed MaxDataSize")

	req := httptest.NewRequest("POST", "/test", strings.NewReader(oversizedBody))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	// The handler should have received the complete body, even though it exceeds MaxDataSize
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, oversizedBody, receivedBody, "Handler should receive the complete body")
	assert.Equal(t, oversizedBody, rr.Body.String(), "Response should echo the complete body")
}

func TestAuditorMiddlewareWithExactMaxSizeBody(t *testing.T) {
	t.Parallel()

	// Use a specific MaxDataSize
	maxSize := 20
	config := &Config{
		IncludeRequestData: true,
		MaxDataSize:        maxSize,
	}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	// Track whether the handler received the complete body
	var receivedBody string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		receivedBody = string(body)
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	})

	middleware := auditor.Middleware(handler)

	// Create a request body with exactly MaxDataSize length
	exactSizeBody := strings.Repeat("x", maxSize)
	require.Equal(t, maxSize, len(exactSizeBody), "Test body must equal MaxDataSize exactly")

	req := httptest.NewRequest("POST", "/test", strings.NewReader(exactSizeBody))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	// The handler should have received the complete body
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, exactSizeBody, receivedBody, "Handler should receive the complete body")
	assert.Equal(t, exactSizeBody, rr.Body.String(), "Response should echo the complete body")
}

func TestAuditorMiddlewareWithEmptyBody(t *testing.T) {
	t.Parallel()

	config := &Config{
		IncludeRequestData: true,
		MaxDataSize:        1024,
	}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	// Track whether the handler was called and received an empty body
	handlerCalled := false
	var receivedBodyLen int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		receivedBodyLen = len(body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	middleware := auditor.Middleware(handler)

	// Create a request with an empty body
	req := httptest.NewRequest("POST", "/test", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	// The handler should have been called with an empty body
	assert.True(t, handlerCalled, "Handler should have been called")
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, 0, receivedBodyLen, "Handler should receive an empty body")
	assert.Equal(t, "OK", rr.Body.String())
}

func TestAuditorMiddlewareWithResponseData(t *testing.T) {
	t.Parallel()
	config := &Config{
		IncludeResponseData: true,
		MaxDataSize:         1024,
	}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	responseData := `{"result": "success"}`
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(responseData))
		require.NoError(t, err)
	})

	middleware := auditor.Middleware(handler)

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()

	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, responseData, rr.Body.String())
}

func TestAuditorMiddlewareWithDifferentSSEPaths(t *testing.T) {
	t.Parallel()
	config := &Config{}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("test response"))
		require.NoError(t, err)
	})

	middleware := auditor.Middleware(handler)

	// Test different SSE paths to ensure transport type detection works correctly
	testPaths := []string{
		"/sse",
		"/v1/sse",
		"/api/sse",
		"/mcp/v2/sse",
		"/events", // Non-SSE path but SSE transport
	}

	for _, path := range testPaths {
		t.Run(fmt.Sprintf("path_%s", strings.ReplaceAll(path, "/", "_")), func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", path, nil)
			rr := httptest.NewRecorder()

			middleware.ServeHTTP(rr, req)

			// All requests should succeed regardless of path since transport type is SSE
			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "test response", rr.Body.String())
		})
	}
}

func TestDetermineEventType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		method    string
		transport string
		expected  string
	}{
		{
			name:      "SSE endpoint",
			path:      "/sse",
			method:    "GET",
			transport: "sse",
			expected:  EventTypeSSEConnection,
		},
		{
			name:      "SSE endpoint with version path",
			path:      "/v1/sse",
			method:    "GET",
			transport: "sse",
			expected:  EventTypeSSEConnection,
		},
		{
			name:      "SSE endpoint with API prefix",
			path:      "/api/sse",
			method:    "GET",
			transport: "sse",
			expected:  EventTypeSSEConnection,
		},
		{
			name:      "SSE endpoint with nested path",
			path:      "/mcp/v2/sse",
			method:    "GET",
			transport: "sse",
			expected:  EventTypeSSEConnection,
		},
		{
			name:      "SSE transport with non-SSE path",
			path:      "/events",
			method:    "GET",
			transport: "sse",
			expected:  EventTypeSSEConnection,
		},
		{
			name:      "MCP messages endpoint",
			path:      "/messages",
			method:    "POST",
			transport: "streamable-http",
			expected:  "http_request", // Since extractMCPMethod returns empty
		},
		{
			name:      "Regular HTTP request",
			path:      "/api/health",
			method:    "GET",
			transport: "streamable-http",
			expected:  "http_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			auditor, err := NewAuditorWithTransport(&Config{}, tt.transport)
			require.NoError(t, err)

			req := httptest.NewRequest(tt.method, tt.path, nil)
			result := auditor.determineEventType(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMapMCPMethodToEventType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mcpMethod string
		expected  string
	}{
		{"initialize", EventTypeMCPInitialize},
		{"tools/call", EventTypeMCPToolCall},
		{"tools/list", EventTypeMCPToolsList},
		{"resources/read", EventTypeMCPResourceRead},
		{"resources/list", EventTypeMCPResourcesList},
		{"prompts/get", EventTypeMCPPromptGet},
		{"prompts/list", EventTypeMCPPromptsList},
		{"notifications/message", EventTypeMCPNotification},
		{"ping", EventTypeMCPPing},
		{"logging/setLevel", EventTypeMCPLogging},
		{"completion/complete", EventTypeMCPCompletion},
		{"notifications/roots/list_changed", EventTypeMCPRootsListChanged},
		{"unknown_method", "mcp_request"},
	}

	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.mcpMethod, func(t *testing.T) {
			t.Parallel()
			result := auditor.mapMCPMethodToEventType(tt.mcpMethod)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetermineOutcome(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	tests := []struct {
		statusCode int
		expected   string
	}{
		{200, OutcomeSuccess},
		{201, OutcomeSuccess},
		{299, OutcomeSuccess},
		{401, OutcomeDenied},
		{403, OutcomeDenied},
		{400, OutcomeFailure},
		{404, OutcomeFailure},
		{429, OutcomeFailure}, // Rate limiting is a load condition, not an identity/policy denial
		{499, OutcomeFailure},
		{500, OutcomeError},
		{503, OutcomeError},
		{100, OutcomeSuccess}, // Default case
	}

	for _, tt := range tests {
		t.Run(string(rune(tt.statusCode)), func(t *testing.T) {
			t.Parallel()
			result := auditor.determineOutcome(tt.statusCode)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetClientIP(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expected   string
	}{
		{
			name:     "X-Forwarded-For header",
			headers:  map[string]string{"X-Forwarded-For": "192.168.1.100, 10.0.0.1"},
			expected: "192.168.1.100",
		},
		{
			name:     "X-Real-IP header",
			headers:  map[string]string{"X-Real-IP": "203.0.113.1"},
			expected: "203.0.113.1",
		},
		{
			name:       "RemoteAddr with port",
			remoteAddr: "192.168.1.50:12345",
			expected:   "192.168.1.50",
		},
		{
			name:       "RemoteAddr without port",
			remoteAddr: "192.168.1.60",
			expected:   "192.168.1.60",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", "/test", nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}

			result := auditor.getClientIP(req)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractSubjects(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	t.Run("with JWT claims", func(t *testing.T) {
		t.Parallel()
		claims := jwt.MapClaims{
			"sub":            "user123",
			"name":           "John Doe",
			"email":          "john@example.com",
			"client_name":    "test-client",
			"client_version": "1.0.0",
		}

		req := httptest.NewRequest("GET", "/test", nil)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: claims["sub"].(string),
				Name:    claims["name"].(string),
				Email:   claims["email"].(string),
				Claims:  claims,
			},
		}
		ctx := auth.WithIdentity(req.Context(), identity)
		req = req.WithContext(ctx)

		subjects := auditor.extractSubjects(req)

		assert.Equal(t, "user123", subjects[SubjectKeyUserID])
		assert.Equal(t, "John Doe", subjects[SubjectKeyUser])
		assert.Equal(t, "test-client", subjects[SubjectKeyClientName])
		assert.Equal(t, "1.0.0", subjects[SubjectKeyClientVersion])
	})

	t.Run("with preferred_username", func(t *testing.T) {
		t.Parallel()
		claims := jwt.MapClaims{
			"sub":                "user456",
			"preferred_username": "johndoe",
		}

		req := httptest.NewRequest("GET", "/test", nil)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: claims["sub"].(string),
				Claims:  claims,
			},
		}
		ctx := auth.WithIdentity(req.Context(), identity)
		req = req.WithContext(ctx)

		subjects := auditor.extractSubjects(req)

		assert.Equal(t, "user456", subjects[SubjectKeyUserID])
		assert.Equal(t, "johndoe", subjects[SubjectKeyUser])
	})

	t.Run("with email fallback", func(t *testing.T) {
		t.Parallel()
		claims := jwt.MapClaims{
			"sub":   "user789",
			"email": "jane@example.com",
		}

		req := httptest.NewRequest("GET", "/test", nil)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: claims["sub"].(string),
				Email:   claims["email"].(string),
				Claims:  claims,
			},
		}
		ctx := auth.WithIdentity(req.Context(), identity)
		req = req.WithContext(ctx)

		subjects := auditor.extractSubjects(req)

		assert.Equal(t, "user789", subjects[SubjectKeyUserID])
		assert.Equal(t, "jane@example.com", subjects[SubjectKeyUser])
	})

	t.Run("without claims", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/test", nil)

		subjects := auditor.extractSubjects(req)

		assert.Equal(t, "anonymous", subjects[SubjectKeyUser])
	})

	t.Run("with delegation chain", func(t *testing.T) {
		t.Parallel()
		claims := jwt.MapClaims{
			"sub":  "user123",
			"name": "John Doe",
			"act": map[string]any{
				"sub": "agent-1",
				"act": map[string]any{"sub": "agent-2"},
			},
		}

		req := httptest.NewRequest("GET", "/test", nil)
		parsed := auth.ParseDelegationChain(claims["act"], auth.DefaultMaxDelegationDepth)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject:         claims["sub"].(string),
				Name:            claims["name"].(string),
				Claims:          claims,
				DelegationChain: &parsed,
			},
		}
		ctx := auth.WithIdentity(req.Context(), identity)
		req = req.WithContext(ctx)

		subjects := auditor.extractSubjects(req)
		assert.Equal(t, "user123", subjects[SubjectKeyUserID])
		assert.Equal(t, "John Doe", subjects[SubjectKeyUser])

		chain := auditor.extractDelegationChain(req)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, 2)
		assert.False(t, chain.Truncated)
		assert.Equal(t, "agent-1", chain.Actors[0].Subject)
		assert.Equal(t, "agent-2", chain.Actors[1].Subject)
	})

	t.Run("delegation chain respects configured max depth", func(t *testing.T) {
		t.Parallel()
		maxDepth := 1
		depthAuditor, err := NewAuditorWithTransport(&Config{MaxDelegationDepth: &maxDepth}, "sse")
		require.NoError(t, err)

		claims := jwt.MapClaims{
			"sub": "user123",
			"act": map[string]any{
				"sub": "agent-1",
				"act": map[string]any{"sub": "agent-2"},
			},
		}

		req := httptest.NewRequest("GET", "/test", nil)
		parsed := auth.ParseDelegationChain(claims["act"], auth.DefaultMaxDelegationDepth)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject:         "user123",
				Claims:          claims,
				DelegationChain: &parsed,
			},
		}
		req = req.WithContext(auth.WithIdentity(req.Context(), identity))

		chain := depthAuditor.extractDelegationChain(req)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, 1, "chain must be capped at the configured max depth")
		assert.Equal(t, "agent-1", chain.Actors[0].Subject)
		assert.True(t, chain.Truncated, "dropped trailing actors must mark the chain truncated")
		assert.Equal(t, 1, chain.DroppedCount, "one dropped actor must be reported")
	})

	t.Run("delegation chain widens through configured max depth", func(t *testing.T) {
		t.Parallel()
		maxDepth := 25
		depthAuditor, err := NewAuditorWithTransport(&Config{MaxDelegationDepth: &maxDepth}, "sse")
		require.NoError(t, err)

		subs := []string{
			"agent-1", "agent-2", "agent-3", "agent-4", "agent-5",
			"agent-6", "agent-7", "agent-8", "agent-9", "agent-10",
			"agent-11", "agent-12", "agent-13", "agent-14",
		}
		claims := jwt.MapClaims{
			"sub": "user123",
			"act": nestedActClaim(subs...),
		}

		req := httptest.NewRequest("GET", "/test", nil)
		// Shaped like what auth.claimsToIdentity produces for a 14-hop token:
		// the identity-layer parse caps at auth.DefaultMaxDelegationDepth (10),
		// truncating the chain even though the raw "act" claim has all 14 hops.
		parsed := auth.ParseDelegationChain(claims["act"], auth.DefaultMaxDelegationDepth)
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject:         "user123",
				Claims:          claims,
				DelegationChain: &parsed,
			},
		}
		req = req.WithContext(auth.WithIdentity(req.Context(), identity))

		chain := depthAuditor.extractDelegationChain(req)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, len(subs), "configured depth must recover actors dropped by the identity-layer parse")
		assert.False(t, chain.Truncated)
	})

	t.Run("no identity yields nil delegation chain", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/test", nil)

		assert.Nil(t, auditor.extractDelegationChain(req))
	})
}

func TestExtractDelegationChainFromIdentity(t *testing.T) {
	t.Parallel()

	t.Run("nil identity", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, extractDelegationChainFromIdentity(nil, auth.DefaultMaxDelegationDepth))
	})

	t.Run("identity without chain", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user123"}}
		assert.Nil(t, extractDelegationChainFromIdentity(identity, auth.DefaultMaxDelegationDepth))
	})

	t.Run("programmatic chain without raw act claim is re-bounded to max depth", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: "user123",
				DelegationChain: &auth.DelegationChain{
					Actors: []auth.DelegatedActor{
						{Subject: "agent-1"},
						{Subject: "agent-2"},
					},
				},
			},
		}
		chain := extractDelegationChainFromIdentity(identity, 1)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, 1, "the parsed chain must be capped at maxDepth")
		assert.Equal(t, "agent-1", chain.Actors[0].Subject)
		assert.True(t, chain.Truncated)
		assert.Equal(t, 1, chain.DroppedCount)
	})

	t.Run("raw act claim without parsed chain is parsed with max depth", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: "user123",
				Claims: map[string]any{
					"act": map[string]any{
						"sub": "agent-1",
						"act": map[string]any{"sub": "agent-2"},
					},
				},
			},
		}
		chain := extractDelegationChainFromIdentity(identity, 1)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, 1, "the raw act claim must be parsed with the depth cap")
		assert.Equal(t, "agent-1", chain.Actors[0].Subject)
		assert.True(t, chain.Truncated)
		assert.Equal(t, 1, chain.DroppedCount)
	})

	t.Run("no chain and no act claim yields nil", func(t *testing.T) {
		t.Parallel()
		identity := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: "user123",
				Claims:  map[string]any{"sub": "user123"},
			},
		}
		assert.Nil(t, extractDelegationChainFromIdentity(identity, auth.DefaultMaxDelegationDepth))
	})

	// The following two cases share an identity shaped exactly like what
	// auth.claimsToIdentity produces for a 14-hop token: the identity-layer
	// parse always caps at auth.DefaultMaxDelegationDepth (10), so
	// DelegationChain holds only the first 10 actors, truncated, with the
	// full 14-hop chain still available in the raw "act" claim.
	subs := []string{
		"agent-1", "agent-2", "agent-3", "agent-4", "agent-5",
		"agent-6", "agent-7", "agent-8", "agent-9", "agent-10",
		"agent-11", "agent-12", "agent-13", "agent-14",
	}
	truncatedIdentity := func() *auth.Identity {
		actors := make([]auth.DelegatedActor, auth.DefaultMaxDelegationDepth)
		for i := range actors {
			actors[i] = auth.DelegatedActor{Subject: subs[i]}
		}
		return &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject: "user123",
				DelegationChain: &auth.DelegationChain{
					Actors:       actors,
					Truncated:    true,
					DroppedCount: len(subs) - auth.DefaultMaxDelegationDepth,
				},
				Claims: map[string]any{"act": nestedActClaim(subs...)},
			},
		}
	}

	t.Run("configured depth wider than identity-layer parse recovers dropped actors", func(t *testing.T) {
		t.Parallel()
		chain := extractDelegationChainFromIdentity(truncatedIdentity(), 25)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, len(subs))
		assert.False(t, chain.Truncated)
		assert.Zero(t, chain.DroppedCount)
		assert.Equal(t, subs[0], chain.Actors[0].Subject)
		assert.Equal(t, subs[len(subs)-1], chain.Actors[len(subs)-1].Subject)
	})

	t.Run("configured depth narrower than identity-layer parse still rebinds", func(t *testing.T) {
		t.Parallel()
		chain := extractDelegationChainFromIdentity(truncatedIdentity(), 3)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, 3)
		assert.True(t, chain.Truncated)
		// 4 actors already dropped by the identity-layer parse (14 - 10),
		// plus 7 more dropped by the narrower rebind (10 - 3).
		assert.Equal(t, 11, chain.DroppedCount)
	})

	t.Run("truncated chain without raw act claim falls back to rebind", func(t *testing.T) {
		t.Parallel()
		identity := truncatedIdentity()
		identity.Claims = nil // widening requested, but no raw claim to re-parse

		chain := extractDelegationChainFromIdentity(identity, 25)
		require.NotNil(t, chain)
		require.Len(t, chain.Actors, auth.DefaultMaxDelegationDepth)
		assert.True(t, chain.Truncated, "without the raw claim, the identity-layer truncation cannot be recovered")
		assert.Equal(t, 4, chain.DroppedCount)
	})
}

// nestedActClaim builds a raw RFC 8693 "act" claim nesting subs in order,
// outermost first, matching the shape auth.ParseDelegationChain expects.
func nestedActClaim(subs ...string) map[string]any {
	var current map[string]any
	for i := len(subs) - 1; i >= 0; i-- {
		m := map[string]any{"sub": subs[i]}
		if current != nil {
			m["act"] = current
		}
		current = m
	}
	return current
}

func TestDetermineComponent(t *testing.T) {
	t.Parallel()
	t.Run("with configured component", func(t *testing.T) {
		t.Parallel()
		config := &Config{Component: "custom-component"}
		auditor, err := NewAuditorWithTransport(config, "sse")
		require.NoError(t, err)

		req := httptest.NewRequest("GET", "/test", nil)
		result := auditor.determineComponent(req)

		assert.Equal(t, "custom-component", result)
	})

	t.Run("without configured component", func(t *testing.T) {
		t.Parallel()
		config := &Config{}
		auditor, err := NewAuditorWithTransport(config, "sse")
		require.NoError(t, err)

		req := httptest.NewRequest("GET", "/test", nil)
		result := auditor.determineComponent(req)

		assert.Equal(t, ComponentToolHive, result)
	})
}

func TestExtractTarget(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	tests := []struct {
		name      string
		path      string
		method    string
		eventType string
		expected  map[string]string
	}{
		{
			name:      "tool call event",
			path:      "/api/tools/calculator",
			method:    "POST",
			eventType: EventTypeMCPToolCall,
			expected: map[string]string{
				TargetKeyEndpoint: "/api/tools/calculator",
				TargetKeyMethod:   "POST",
				TargetKeyType:     TargetTypeTool,
			},
		},
		{
			name:      "resource read event",
			path:      "/api/resources/file.txt",
			method:    "GET",
			eventType: EventTypeMCPResourceRead,
			expected: map[string]string{
				TargetKeyEndpoint: "/api/resources/file.txt",
				TargetKeyMethod:   "GET",
				TargetKeyType:     TargetTypeResource,
			},
		},
		{
			name:      "generic event",
			path:      "/api/health",
			method:    "GET",
			eventType: "http_request",
			expected: map[string]string{
				TargetKeyEndpoint: "/api/health",
				TargetKeyMethod:   "GET",
				TargetKeyType:     "endpoint",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			result := auditor.extractTarget(req, tt.eventType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAddMetadata(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
	duration := 150 * time.Millisecond
	rw := &responseWriter{
		ResponseWriter: httptest.NewRecorder(),
		body:           bytes.NewBufferString("test response"),
	}
	req := httptest.NewRequest("GET", "/test", nil)

	auditor.addMetadata(event, req, duration, rw)

	require.NotNil(t, event.Metadata.Extra)
	assert.Equal(t, int64(150), event.Metadata.Extra[MetadataExtraKeyDuration])
	assert.Equal(t, "sse", event.Metadata.Extra[MetadataExtraKeyTransport])
	assert.Equal(t, 13, event.Metadata.Extra[MetadataExtraKeyResponseSize]) // "test response" length
}

func TestAddEventData(t *testing.T) {
	t.Parallel()
	t.Run("with request and response data", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			IncludeRequestData:  true,
			IncludeResponseData: true,
		}
		auditor, err := NewAuditorWithTransport(config, "sse")
		require.NoError(t, err)

		event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
		req := httptest.NewRequest("POST", "/test", nil)
		requestData := []byte(`{"input": "test"}`)
		rw := &responseWriter{
			body: bytes.NewBufferString(`{"output": "result"}`),
		}

		auditor.addEventData(event, req, rw, requestData)

		require.NotNil(t, event.Data)

		var data map[string]any
		err = json.Unmarshal(*event.Data, &data)
		require.NoError(t, err)

		requestObj, ok := data["request"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "test", requestObj["input"])

		responseObj, ok := data["response"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "result", responseObj["output"])
	})

	t.Run("with non-JSON data", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			IncludeRequestData:  true,
			IncludeResponseData: true,
		}
		auditor, err := NewAuditorWithTransport(config, "sse")
		require.NoError(t, err)

		event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
		req := httptest.NewRequest("POST", "/test", nil)
		requestData := []byte("plain text request")
		rw := &responseWriter{
			body: bytes.NewBufferString("plain text response"),
		}

		auditor.addEventData(event, req, rw, requestData)

		require.NotNil(t, event.Data)

		var data map[string]any
		err = json.Unmarshal(*event.Data, &data)
		require.NoError(t, err)

		assert.Equal(t, "plain text request", data["request"])
		assert.Equal(t, "plain text response", data["response"])
	})

	t.Run("disabled data inclusion", func(t *testing.T) {
		t.Parallel()
		config := &Config{
			IncludeRequestData:  false,
			IncludeResponseData: false,
		}
		auditor, err := NewAuditorWithTransport(config, "sse")
		require.NoError(t, err)

		event := NewAuditEvent("test", EventSource{}, OutcomeSuccess, map[string]string{}, "test")
		req := httptest.NewRequest("POST", "/test", nil)
		requestData := []byte("test data")
		rw := &responseWriter{body: bytes.NewBufferString("response")}

		auditor.addEventData(event, req, rw, requestData)

		assert.Nil(t, event.Data)
	})
}

func TestResponseWriterCapture(t *testing.T) {
	t.Parallel()
	config := &Config{
		IncludeResponseData: true,
		MaxDataSize:         10, // Small limit for testing
	}
	auditor, err := NewAuditorWithTransport(config, "sse")
	require.NoError(t, err)

	rw := &responseWriter{
		ResponseWriter: httptest.NewRecorder(),
		auditor:        auditor,
		body:           &bytes.Buffer{},
	}

	// Write data within limit
	n, err := rw.Write([]byte("test"))
	assert.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "test", rw.body.String())

	// Write data that exceeds limit
	n, err = rw.Write([]byte("more data"))
	assert.NoError(t, err)
	assert.Equal(t, 9, n)
	// Should not capture more data due to size limit
	assert.Equal(t, "test", rw.body.String())
}

func TestResponseWriterStatusCode(t *testing.T) {
	t.Parallel()
	rw := &responseWriter{
		ResponseWriter: httptest.NewRecorder(),
		statusCode:     http.StatusOK, // Default
	}

	// Test WriteHeader
	rw.WriteHeader(http.StatusCreated)
	assert.Equal(t, http.StatusCreated, rw.statusCode)
}

func TestExtractSourceWithHeaders(t *testing.T) {
	t.Parallel()
	auditor, err := NewAuditorWithTransport(&Config{}, "sse")
	require.NoError(t, err)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("User-Agent", "TestAgent/1.0")
	req.Header.Set("X-Request-ID", "req-12345")
	req.RemoteAddr = "192.168.1.100:8080"

	source := auditor.extractSource(req)

	assert.Equal(t, SourceTypeNetwork, source.Type)
	assert.Equal(t, "192.168.1.100", source.Value)
	assert.Equal(t, "TestAgent/1.0", source.Extra[SourceExtraKeyUserAgent])
	assert.Equal(t, "req-12345", source.Extra[SourceExtraKeyRequestID])
}

func TestErrorDetectionBodyCapture(t *testing.T) {
	t.Parallel()

	t.Run("captures prefix when DetectApplicationErrors is enabled", func(t *testing.T) {
		t.Parallel()
		detectErrors := true
		config := &Config{
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)

		rw := &responseWriter{
			ResponseWriter:     httptest.NewRecorder(),
			statusCode:         http.StatusOK,
			auditor:            auditor,
			errorDetectionBody: &bytes.Buffer{},
		}

		responseData := `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"test error"}}`
		_, err = rw.Write([]byte(responseData))
		require.NoError(t, err)

		assert.Equal(t, responseData, rw.errorDetectionBody.String())
	})

	t.Run("does not capture when DetectApplicationErrors is disabled", func(t *testing.T) {
		t.Parallel()
		detectErrors := false
		config := &Config{
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)

		rw := &responseWriter{
			ResponseWriter: httptest.NewRecorder(),
			statusCode:     http.StatusOK,
			auditor:        auditor,
			// errorDetectionBody is nil when detection is disabled
		}

		_, err = rw.Write([]byte(`{"error":{"code":-32603}}`))
		require.NoError(t, err)

		assert.Nil(t, rw.errorDetectionBody)
	})

	t.Run("truncates capture at buffer size limit", func(t *testing.T) {
		t.Parallel()
		detectErrors := true
		config := &Config{
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)

		rw := &responseWriter{
			ResponseWriter:     httptest.NewRecorder(),
			statusCode:         http.StatusOK,
			auditor:            auditor,
			errorDetectionBody: &bytes.Buffer{},
		}

		// Write more than errorDetectionBufferSize bytes
		largeData := bytes.Repeat([]byte("x"), errorDetectionBufferSize+100)
		_, err = rw.Write(largeData)
		require.NoError(t, err)

		assert.Equal(t, errorDetectionBufferSize, rw.errorDetectionBody.Len())
	})

	t.Run("captures independently of IncludeResponseData", func(t *testing.T) {
		t.Parallel()
		detectErrors := true
		config := &Config{
			IncludeResponseData:     false,
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)

		rw := &responseWriter{
			ResponseWriter:     httptest.NewRecorder(),
			statusCode:         http.StatusOK,
			auditor:            auditor,
			errorDetectionBody: &bytes.Buffer{},
			// body is nil because IncludeResponseData is false
		}

		responseData := `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"unauthorized"}}`
		_, err = rw.Write([]byte(responseData))
		require.NoError(t, err)

		// errorDetectionBody should capture even though body is nil
		assert.Equal(t, responseData, rw.errorDetectionBody.String())
		assert.Nil(t, rw.body)
	})
}

func TestMiddlewareDetectsJSONRPCErrors(t *testing.T) {
	t.Parallel()

	t.Run("overrides outcome to application_error for JSON-RPC error response", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		detectErrors := true
		config := &Config{
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)
		auditor.auditLogger = NewAuditLogger(&logBuf)

		errorResponse := `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"GitLab API error: 401 Unauthorized"}}`
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(errorResponse))
			require.NoError(t, err)
		})

		middleware := auditor.Middleware(handler)
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"test"}}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		middleware.ServeHTTP(rr, req)

		// The response should still be passed through unchanged
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, errorResponse, rr.Body.String())

		// The audit log should contain application_error
		logOutput := logBuf.String()
		assert.Contains(t, logOutput, OutcomeApplicationError)
		assert.Contains(t, logOutput, "jsonrpc_error_code")
	})

	t.Run("keeps outcome=success for valid JSON-RPC result", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		detectErrors := true
		config := &Config{
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)
		auditor.auditLogger = NewAuditLogger(&logBuf)

		successResponse := `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"hello"}]}}`
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(successResponse))
			require.NoError(t, err)
		})

		middleware := auditor.Middleware(handler)
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"test"}}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		middleware.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		logOutput := logBuf.String()
		assert.NotContains(t, logOutput, OutcomeApplicationError)
	})

	t.Run("does not inspect body when DetectApplicationErrors is disabled", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		detectErrors := false
		config := &Config{
			DetectApplicationErrors: &detectErrors,
		}
		auditor, err := NewAuditorWithTransport(config, "streamable-http")
		require.NoError(t, err)
		auditor.auditLogger = NewAuditLogger(&logBuf)

		errorResponse := `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"should not be detected"}}`
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(errorResponse))
			require.NoError(t, err)
		})

		middleware := auditor.Middleware(handler)
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/call","params":{"name":"test"}}`))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()

		middleware.ServeHTTP(rr, req)

		logOutput := logBuf.String()
		assert.NotContains(t, logOutput, OutcomeApplicationError)
	})
}

func TestAuditLoggerLevelFormat(t *testing.T) {
	t.Parallel()

	t.Run("renders audit level as AUDIT not INFO+2", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		logger := NewAuditLogger(&logBuf)

		// Log an audit event
		logger.Log(context.TODO(), LevelAudit, "test audit event",
			"audit_id", "test-123",
			"type", "test_event")

		logOutput := logBuf.String()
		// Should contain "AUDIT" not "INFO+2"
		assert.Contains(t, logOutput, `"level":"AUDIT"`)
		assert.NotContains(t, logOutput, "INFO+2")
		assert.NotContains(t, logOutput, "INFO+")
	})

	t.Run("preserves standard log levels", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		logger := NewAuditLogger(&logBuf)

		// NewAuditLogger sets Level: LevelAudit (= slog.Level(2)), so INFO
		// (slog.Level(0)) is filtered out before ReplaceAttr runs and can't
		// be exercised through the production logger. WARN (slog.Level(4))
		// is above LevelAudit and passes through, so we use it to confirm
		// the production ReplaceAttr path doesn't mis-label non-audit events.
		logger.Warn("warn message")
		logOutput := logBuf.String()
		assert.Contains(t, logOutput, `"level":"WARN"`)
		assert.NotContains(t, logOutput, `"level":"AUDIT"`)
	})
}

// newBufferAuditor returns an Auditor writing audit events to the returned
// buffer, for tests asserting on emitted events.
func newBufferAuditor(t *testing.T) (*Auditor, *bytes.Buffer) {
	t.Helper()
	return newBufferAuditorWithConfig(t, &Config{Component: "test"})
}

// newBufferAuditorWithConfig is newBufferAuditor for tests that need a
// non-default Config (e.g. MaxDelegationDepth).
func newBufferAuditorWithConfig(t *testing.T, cfg *Config) (*Auditor, *bytes.Buffer) {
	t.Helper()
	auditor, err := NewAuditorWithTransport(cfg, "streamable-http")
	require.NoError(t, err)
	var logBuf bytes.Buffer
	auditor.auditLogger = NewAuditLogger(&logBuf)
	return auditor, &logBuf
}

// decodeAuditEvents parses the newline-delimited JSON events in buf.
func decodeAuditEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event), "audit log line is not JSON: %s", line)
		events = append(events, event)
	}
	return events
}

// newToolsCallRequest builds a POST tools/call request suitable for the parser.
func newToolsCallRequest() *http.Request {
	req := httptest.NewRequest("POST", "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"target_tool","arguments":{}}}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestMiddlewareAuditsInnerChainOutcomes pins the audit-wraps-chain
// arrangement: auth and the MCP parser run INSIDE the audit middleware, and
// audit reads the identity and parsed MCP data back through the holder
// carriers it injects (auth.IdentityHolder, mcp.ParsedRequestHolder). This is
// what lets audit record rejections from any inner middleware — auth 401s,
// webhook denials, authz 403s — instead of only requests that reached its old
// position deep in the chain.
func TestMiddlewareAuditsInnerChainOutcomes(t *testing.T) {
	t.Parallel()

	// innerAuth emulates an auth middleware running inside audit: it attaches
	// the identity exactly as production middlewares do (via auth.WithIdentity,
	// which also fills the IdentityHolder injected by audit).
	innerAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
				Subject: "user-123",
				Name:    "Test User",
				Claims:  jwt.MapClaims{"sub": "user-123"},
			}}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}

	t.Run("identity attached by inner auth reaches the audit event", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		auditor.Middleware(innerAuth(handler)).ServeHTTP(httptest.NewRecorder(), newToolsCallRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)
		assert.Equal(t, OutcomeSuccess, events[0]["outcome"])
		subjects, ok := events[0]["subjects"].(map[string]any)
		require.True(t, ok, "event must carry subjects")
		assert.Equal(t, "user-123", subjects[SubjectKeyUserID],
			"identity attached by an inner auth middleware must reach the audit event via the holder")
	})

	t.Run("inner 401 rejection is audited as denied with anonymous subject", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		reject := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
		})
		auditor.Middleware(reject).ServeHTTP(httptest.NewRecorder(), newToolsCallRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1, "an authentication failure must still produce an audit event")
		assert.Equal(t, OutcomeDenied, events[0]["outcome"])
		subjects, ok := events[0]["subjects"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "anonymous", subjects[SubjectKeyUser],
			"no identity exists when authentication fails")
	})

	t.Run("event type comes from inner parser via holder", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		// The parser runs INSIDE audit, as in the real chains.
		auditor.Middleware(mcp.ParsingMiddleware(handler)).ServeHTTP(httptest.NewRecorder(), newToolsCallRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)
		assert.Equal(t, EventTypeMCPToolCall, events[0]["type"],
			"parsed MCP data from an inner parser must drive the event type via the holder")
		target, ok := events[0]["target"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "target_tool", target[TargetKeyName])
	})
}

// TestLogAuditEventDelegationChain pins the emitted delegation_chain shape on
// the primary logAuditEvent path (every ordinary POST). The SSE path already
// has an equivalent pin (see "delegated token stream open carries the
// delegation chain" in TestStreamOpenAuditEvents) and workflow auditing has
// its own (workflow_auditor_test.go), but logAuditEvent had none, so a
// pkg/audit regression here was only ever caught by running pkg/authserver's
// suite. What this test uniquely pins beyond those two siblings is that the
// POST path honours Config.MaxDelegationDepth via
// Config.MaxDelegationDepthOrDefault at this specific call site.
func TestLogAuditEventDelegationChain(t *testing.T) {
	t.Parallel()

	// twoHopIdentity builds an auth.Identity carrying a hand-built two-actor
	// RFC 8693 chain, outermost (most recent) first: agent-2 delegated from
	// agent-1, with agent-1's act claim also carrying an extra "iss" member to
	// pin the documented act_claims shape.
	twoHopIdentity := func() *auth.Identity {
		act := map[string]any{
			"sub": "agent-2",
			"act": map[string]any{"sub": "agent-1", "iss": "https://issuer.example"},
		}
		chain := auth.ParseDelegationChain(act, auth.DefaultMaxDelegationDepth)
		return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Subject:         "user-123",
			Claims:          map[string]any{"act": act},
			DelegationChain: &chain,
		}}
	}

	t.Run("full documented shape: two actors, one carrying act_claims", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)
		identity := twoHopIdentity()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = auth.WithIdentity(r.Context(), identity)
			w.WriteHeader(http.StatusOK)
		})
		auditor.Middleware(handler).ServeHTTP(httptest.NewRecorder(), newToolsCallRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)

		chain, ok := events[0]["delegation_chain"].(map[string]any)
		require.True(t, ok, "the POST/logAuditEvent path must carry the delegation chain")
		assert.Equal(t, false, chain["truncated"])
		_, hasDropped := chain["dropped_count"]
		assert.False(t, hasDropped, "dropped_count must be omitted when nothing was dropped")

		actors, ok := chain["actors"].([]any)
		require.True(t, ok, "actors should be an array")
		require.Len(t, actors, 2)

		outer, ok := actors[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "agent-2", outer["sub"], "actors[0] must be the outermost/most recent actor")
		_, hasClaims := outer["act_claims"]
		assert.False(t, hasClaims, "the outer actor here carries no extra act members")

		inner, ok := actors[1].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "agent-1", inner["sub"])
		actClaims, ok := inner["act_claims"].(map[string]any)
		require.True(t, ok, "the documented act_claims member must surface extra act-claim data")
		assert.Equal(t, "https://issuer.example", actClaims["iss"])
	})

	t.Run("MaxDelegationDepth truncates the chain and keeps the outermost actor", func(t *testing.T) {
		t.Parallel()
		maxDepth := 1
		auditor, logBuf := newBufferAuditorWithConfig(t, &Config{Component: "test", MaxDelegationDepth: &maxDepth})
		identity := twoHopIdentity()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = auth.WithIdentity(r.Context(), identity)
			w.WriteHeader(http.StatusOK)
		})
		auditor.Middleware(handler).ServeHTTP(httptest.NewRecorder(), newToolsCallRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)

		chain, ok := events[0]["delegation_chain"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, true, chain["truncated"])
		assert.Equal(t, float64(1), chain["dropped_count"],
			"one of the two actors must be reported dropped")

		actors, ok := chain["actors"].([]any)
		require.True(t, ok)
		require.Len(t, actors, 1, "MaxDelegationDepth=1 must leave exactly one surviving actor")
		surviving, ok := actors[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "agent-2", surviving["sub"], "the surviving actor must be the outermost one")
	})

	t.Run("plain identity omits the delegation chain", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123"}}

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = auth.WithIdentity(r.Context(), identity)
			w.WriteHeader(http.StatusOK)
		})
		auditor.Middleware(handler).ServeHTTP(httptest.NewRecorder(), newToolsCallRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)

		_, exists := events[0]["delegation_chain"]
		assert.False(t, exists,
			"a non-delegated identity must not produce a delegation_chain member on the POST path")
	})
}

// TestStreamOpenAuditEvents pins the deferred stream-open logging: the
// connection event for SSE / streamable GET requests is logged on the FIRST
// response write, so it reflects the real outcome and the identity attached by
// inner middleware — instead of being logged on arrival with neither.
func TestStreamOpenAuditEvents(t *testing.T) {
	t.Parallel()

	newStreamRequest := func() *http.Request {
		req := httptest.NewRequest("GET", "/mcp", nil)
		req.Header.Set("Accept", "text/event-stream")
		return req
	}

	t.Run("established stream logs one success event with identity", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Emulate inner auth attaching the identity before the stream starts.
			// The returned context is intentionally discarded: only WithIdentity's
			// side effect of filling the audit-injected IdentityHolder matters here.
			id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123"}}
			_ = auth.WithIdentity(r.Context(), id)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: message\ndata: {}\n\n"))
			_, _ = w.Write([]byte("event: message\ndata: {}\n\n"))
		})
		auditor.Middleware(handler).ServeHTTP(httptest.NewRecorder(), newStreamRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1, "the connection event must be logged exactly once")
		assert.Equal(t, EventTypeSSEConnection, events[0]["type"])
		assert.Equal(t, OutcomeSuccess, events[0]["outcome"])
		subjects, ok := events[0]["subjects"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "user-123", subjects[SubjectKeyUserID],
			"deferring the log to first write makes the inner auth identity available")
	})

	t.Run("rejected stream open logs a denied event", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		reject := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
		})
		auditor.Middleware(reject).ServeHTTP(httptest.NewRecorder(), newStreamRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)
		assert.Equal(t, EventTypeSSEConnection, events[0]["type"])
		assert.Equal(t, OutcomeDenied, events[0]["outcome"])
	})

	t.Run("handler that never writes still produces one event", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		silent := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
		auditor.Middleware(silent).ServeHTTP(httptest.NewRecorder(), newStreamRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)
		assert.Equal(t, OutcomeSuccess, events[0]["outcome"],
			"net/http sends an implicit 200 when the handler writes nothing")
	})

	t.Run("flush before first write logs the connection event", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		// A handler that establishes the stream by flushing headers and then
		// blocks (waiting for events to send) never hits WriteHeader/Write.
		flusher := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		})
		auditor.Middleware(flusher).ServeHTTP(httptest.NewRecorder(), newStreamRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1, "the flush must not bypass the connection event")
		assert.Equal(t, EventTypeSSEConnection, events[0]["type"])
		assert.Equal(t, OutcomeSuccess, events[0]["outcome"])
	})

	t.Run("panic before first write still logs the connection event", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		panicker := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			panic("boom before first write")
		})
		handler := auditor.Middleware(panicker)
		require.Panics(t, func() {
			handler.ServeHTTP(httptest.NewRecorder(), newStreamRequest())
		}, "no recovery middleware on this chain: the panic must propagate")

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1,
			"chains whose recovery middleware runs OUTSIDE audit (e.g. the vMCP Serve path) "+
				"must not lose the connection event to a panic")
		assert.Equal(t, EventTypeSSEConnection, events[0]["type"])
	})

	t.Run("delegated token stream open carries the delegation chain", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		act := map[string]any{
			"sub": "agent-1",
			"act": map[string]any{"sub": "agent-2"},
		}
		chain := auth.ParseDelegationChain(act, auth.DefaultMaxDelegationDepth)
		delegated := &auth.Identity{
			PrincipalInfo: auth.PrincipalInfo{
				Subject:         "user-123",
				Claims:          map[string]any{"act": act},
				DelegationChain: &chain,
			},
		}

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Emulate inner auth attaching the identity before the stream starts.
			// The returned context is intentionally discarded: only WithIdentity's
			// side effect of filling the audit-injected IdentityHolder matters here.
			_ = auth.WithIdentity(r.Context(), delegated)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: message\ndata: {}\n\n"))
		})
		auditor.Middleware(handler).ServeHTTP(httptest.NewRecorder(), newStreamRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)
		assert.Equal(t, EventTypeSSEConnection, events[0]["type"])

		logged, ok := events[0]["delegation_chain"].(map[string]any)
		require.True(t, ok, "the SSE connection event must carry the delegation chain")
		assert.Equal(t, false, logged["truncated"])
		actors, ok := logged["actors"].([]any)
		require.True(t, ok)
		require.Len(t, actors, 2)
		first, ok := actors[0].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "agent-1", first["sub"])
		second, ok := actors[1].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "agent-2", second["sub"])
	})

	t.Run("non-delegated identity stream open omits the delegation chain", func(t *testing.T) {
		t.Parallel()
		auditor, logBuf := newBufferAuditor(t)

		plain := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123"}}

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Emulate inner auth attaching the identity before the stream starts.
			// The returned context is intentionally discarded: only WithIdentity's
			// side effect of filling the audit-injected IdentityHolder matters here.
			_ = auth.WithIdentity(r.Context(), plain)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("event: message\ndata: {}\n\n"))
		})
		auditor.Middleware(handler).ServeHTTP(httptest.NewRecorder(), newStreamRequest())

		events := decodeAuditEvents(t, logBuf)
		require.Len(t, events, 1)
		assert.Equal(t, EventTypeSSEConnection, events[0]["type"])

		subjects, ok := events[0]["subjects"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "user-123", subjects[SubjectKeyUserID], "the identity must have landed")

		_, exists := events[0]["delegation_chain"]
		assert.False(t, exists,
			"an authenticated non-delegated identity must not produce a delegation_chain member on the stream-open path")
	})
}

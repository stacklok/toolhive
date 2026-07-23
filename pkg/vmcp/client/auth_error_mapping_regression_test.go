// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// jsonRPCResponse is a generic JSON-RPC 2.0 response envelope.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonRPCRequest is a generic JSON-RPC 2.0 request envelope (for method routing).
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
}

// TestRegression_401_MapsToErrAuthenticationFailed verifies that a backend
// returning HTTP 401 on initialize is classified as ErrAuthenticationFailed.
func TestRegression_401_MapsToErrAuthenticationFailed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32000, Message: "Unauthorized"},
		})
	}))
	t.Cleanup(srv.Close)

	h := &httpBackendClient{
		clientFactory: func(ctx context.Context, target *vmcp.BackendTarget, _ bool) (*client.Client, error) {
			c, err := client.NewStreamableHttpClient(
				target.BaseURL,
				mcptransport.WithHTTPTimeout(30*time.Second),
			)
			if err != nil {
				return nil, err
			}
			if err := c.Start(ctx); err != nil {
				return nil, err
			}
			return c, nil
		},
	}

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-401-backend",
		WorkloadName:  "Test 401 Backend",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	// Pre-seed Legacy so ListCapabilities skips the Modern discover probe (which
	// needs a real registry) and exercises the Legacy error-mapping path here.
	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)

	_, err := h.ListCapabilities(context.Background(), target)
	require.Error(t, err)
	assert.True(t, errors.Is(err, vmcp.ErrAuthenticationFailed),
		"expected ErrAuthenticationFailed, got: %v", err)
}

// TestRegression_403OnInitialize_LegacySSEFallback verifies that a backend
// returning HTTP 403 on initialize is classified as ErrBackendUnavailable.
//
// NOTE: The mcp-go streamable-HTTP transport returns a generic HTTP error for
// 403 ("request failed with status 403"), not transport.ErrLegacySSEServer.
// The "legacy SSE" hint in wrapBackendError is only added when the origin error
// IS transport.ErrLegacySSEServer (returned by SSE transport, not streamable-HTTP).
// For streamable-HTTP, 403 falls through to string-based classification and
// correctly maps to ErrBackendUnavailable, but without the SSE-specific message.
func TestRegression_403OnInitialize_LegacySSEFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32000, Message: "Forbidden"},
		})
	}))
	t.Cleanup(srv.Close)

	h := &httpBackendClient{
		clientFactory: func(ctx context.Context, target *vmcp.BackendTarget, _ bool) (*client.Client, error) {
			c, err := client.NewStreamableHttpClient(
				target.BaseURL,
				mcptransport.WithHTTPTimeout(30*time.Second),
			)
			if err != nil {
				return nil, err
			}
			if err := c.Start(ctx); err != nil {
				return nil, err
			}
			return c, nil
		},
	}

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-403-backend",
		WorkloadName:  "Test 403 Backend",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)

	_, err := h.ListCapabilities(context.Background(), target)
	require.Error(t, err)
	assert.True(t, errors.Is(err, vmcp.ErrBackendUnavailable),
		"expected ErrBackendUnavailable, got: %v", err)
	assert.Contains(t, err.Error(), "403",
		"error message should reference 403 status, got: %v", err)
}

// TestRegression_403OnInitialize_MatchesSentinel verifies that
// transport.ErrLegacySSEServer is NOT in the error chain for 403 on
// initialize, because wrapBackendError uses %v (not %w) for the
// original error, AND the mcp-go streamable-HTTP transport does not
// return ErrLegacySSEServer for 403 (it returns a generic HTTP error).
// Regardless of which error type is at the origin, the sentinel should
// never be in the chain.
func TestRegression_403OnInitialize_MatchesSentinel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: -32000, Message: "Forbidden"},
		})
	}))
	t.Cleanup(srv.Close)

	h := &httpBackendClient{
		clientFactory: func(ctx context.Context, target *vmcp.BackendTarget, _ bool) (*client.Client, error) {
			c, err := client.NewStreamableHttpClient(
				target.BaseURL,
				mcptransport.WithHTTPTimeout(30*time.Second),
			)
			if err != nil {
				return nil, err
			}
			if err := c.Start(ctx); err != nil {
				return nil, err
			}
			return c, nil
		},
	}

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-403-sentinel-backend",
		WorkloadName:  "Test 403 Sentinel Backend",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)

	_, err := h.ListCapabilities(context.Background(), target)
	require.Error(t, err)

	// wrapBackendError uses %v for the original error, so
	// transport.ErrLegacySSEServer is NOT in the chain.
	assert.False(t, errors.Is(err, mcptransport.ErrLegacySSEServer),
		"transport.ErrLegacySSEServer should NOT be in the error chain (wrapBackendError uses %v)")
}

// TestRegression_BackendToolErrorWith401_NotClassifiedAsAuthFailure verifies
// that an MCP tool error result (IsError=true on a 200 HTTP response) whose
// message contains "401 unauthorized" is NOT classified as an auth failure.
func TestRegression_BackendToolErrorWith401_NotClassifiedAsAuthFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"protocolVersion": "2024-11-05",
					"capabilities": {"tools": {}},
					"serverInfo": {"name": "test-backend", "version": "1.0.0"}
				}`),
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "tools/call":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"content": [{"type": "text", "text": "tool error: 401 unauthorized - permission denied"}],
					"isError": true
				}`),
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"tools": [{"name": "test-tool", "description": "A test tool", "inputSchema": {"type": "object"}}]
				}`),
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{}`),
			})
		}
	}))
	t.Cleanup(srv.Close)

	h := &httpBackendClient{
		clientFactory: func(ctx context.Context, target *vmcp.BackendTarget, _ bool) (*client.Client, error) {
			c, err := client.NewStreamableHttpClient(
				target.BaseURL,
				mcptransport.WithHTTPTimeout(30*time.Second),
			)
			if err != nil {
				return nil, err
			}
			if err := c.Start(ctx); err != nil {
				return nil, err
			}
			return c, nil
		},
	}

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-tool-error-backend",
		WorkloadName:  "Test Tool Error Backend",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := h.CallTool(ctx, target, "test-tool", map[string]any{"arg": "val"}, nil)

	if err != nil {
		if errors.Is(err, vmcp.ErrAuthenticationFailed) {
			t.Fatalf("UNEXPECTED: CallTool returned ErrAuthenticationFailed for a 200 response with IsError=true containing '401 unauthorized'")
		}
		t.Fatalf("unexpected transport error from CallTool: %v", err)
	}

	assert.NotNil(t, result, "expected non-nil result for IsError=true")
	assert.True(t, result.IsError, "expected IsError=true")
	assert.NotEmpty(t, result.Content, "expected non-empty content")
}

// TestRegression_WrapBackendError_ClassificationMatrix pins the remaining
// wrapBackendError classification arms that the httptest-driven cases above do
// not exercise. wrapBackendError maps several distinct failure shapes onto vmcp
// sentinels, and health monitoring (#4935, #5223) branches on those sentinels;
// a regression that reshuffles the arms would silently mis-route recovery. Each
// case reconstructs the error shape the transport layer produces in the wild and
// asserts the sentinel via errors.Is (the chain-aware check wrapBackendError's
// %w wrapping is designed to satisfy).
func TestRegression_WrapBackendError_ClassificationMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantSentinel error
	}{
		{
			// A backend returning a 5xx surfaces as a generic HTTP status error
			// (no transport sentinel). It must classify as ErrBackendUnavailable
			// so the health monitor can recover once the backend is healthy again.
			name:         "5xx HTTP status maps to ErrBackendUnavailable",
			err:          fmt.Errorf("request failed with status code 503 service unavailable"),
			wantSentinel: vmcp.ErrBackendUnavailable,
		},
		{
			// A network timeout arrives as a net.Error whose Timeout() reports true;
			// wrapBackendError's errors.As(&netErr)+Timeout() arm must map it to
			// ErrTimeout ahead of any string-based fallback.
			name:         "net.Error timeout maps to ErrTimeout",
			err:          &net.DNSError{Err: "i/o timeout", Name: "backend.example", IsTimeout: true},
			wantSentinel: vmcp.ErrTimeout,
		},
		{
			// An unexpectedly closed connection surfaces as io.EOF; it means the
			// backend dropped the stream, so it maps to ErrBackendUnavailable.
			name:         "io.EOF maps to ErrBackendUnavailable",
			err:          io.EOF,
			wantSentinel: vmcp.ErrBackendUnavailable,
		},
		{
			// A refused dial (backend down) is a net.OpError whose message contains
			// "connection refused"; the Timeout() arm is skipped (not a timeout) and
			// the connection-error string arm maps it to ErrBackendUnavailable.
			name: "connection refused maps to ErrBackendUnavailable",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: errors.New("connect: connection refused"),
			},
			wantSentinel: vmcp.ErrBackendUnavailable,
		},
		{
			// transport.ErrAuthorizationRequired is the migration-sensitive sentinel
			// (mcp-go v0.49.0+) returned for 401 + WWW-Authenticate. It must map to
			// ErrAuthenticationFailed so health monitoring engages the auth-aware
			// branch (#4935) instead of marking the backend unhealthy (#5223).
			name:         "transport.ErrAuthorizationRequired maps to ErrAuthenticationFailed",
			err:          mcptransport.ErrAuthorizationRequired,
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
		{
			// The production chain wraps the sentinel in *transport.Error via
			// *AuthorizationRequiredError; both layers Unwrap to the sentinel so
			// errors.Is must still classify it as ErrAuthenticationFailed.
			name:         "wrapped AuthorizationRequiredError maps to ErrAuthenticationFailed",
			err:          mcptransport.NewError(&mcptransport.AuthorizationRequiredError{}),
			wantSentinel: vmcp.ErrAuthenticationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := wrapBackendError(tt.err, "test-backend", "initialize")
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantSentinel),
				"expected %v, got: %v", tt.wantSentinel, err)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// maxJSONRPCResponseBytes caps how much of an upstream JSON-RPC response the proxy
// will buffer for structural validation. Matches existing streamable-HTTP body
// limits elsewhere in the codebase (pkg/vmcp/client, pkg/vmcp/session/internal/backend).
const maxJSONRPCResponseBytes = 100 << 20 // 100 MiB

// JSON-RPC error code returned to clients when the proxy rejects a malformed
// upstream response. -32000..-32099 is the implementation-defined server-error
// range in the JSON-RPC 2.0 spec; -32603 is reserved for internal JSON-RPC
// implementation errors and is not appropriate for a policy-level rejection.
const jsonRPCInvalidUpstreamCode = -32000

// ResponseProcessor defines the interface for processing and modifying HTTP responses
// based on transport-specific requirements.
type ResponseProcessor interface {
	// ProcessResponse modifies an HTTP response based on transport-specific logic.
	// Returns error if processing fails.
	ProcessResponse(resp *http.Response) error

	// ShouldProcess returns true if this processor should handle the given response.
	ShouldProcess(resp *http.Response) bool
}

// NoOpResponseProcessor is the default processor for non-SSE transports.
// It validates JSON-RPC responses for streamable HTTP and otherwise leaves responses unchanged.
type NoOpResponseProcessor struct{}

// ProcessResponse validates JSON-RPC responses when applicable.
func (*NoOpResponseProcessor) ProcessResponse(resp *http.Response) error {
	if !shouldValidateJSONRPCResponse(resp) {
		return nil
	}

	// Read one byte past the cap so we can detect oversize without allocating beyond it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONRPCResponseBytes+1))
	if err != nil {
		return fmt.Errorf("failed to read upstream response body: %w", err)
	}
	_ = resp.Body.Close()

	if len(body) > maxJSONRPCResponseBytes {
		writeInvalidUpstreamJSONRPCResponse(resp, fmt.Errorf(
			"upstream JSON-RPC response exceeds maximum allowed size of %d bytes", maxJSONRPCResponseBytes))
		return nil
	}

	if err := mcpparser.ValidateJSONRPCResponseBody(body); err != nil {
		writeInvalidUpstreamJSONRPCResponse(resp, err)
		return nil
	}

	// The reverse proxy still needs a readable body after validation.
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	return nil
}

// ShouldProcess always returns false for no-op processor.
func (*NoOpResponseProcessor) ShouldProcess(_ *http.Response) bool {
	return false
}

func shouldValidateJSONRPCResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil || resp.Request == nil {
		return false
	}
	if resp.Request.Method != http.MethodPost || resp.StatusCode != http.StatusOK {
		return false
	}
	if !hasIdentityContentEncoding(resp.Header.Get("Content-Encoding")) {
		// Content-Encoding semantics (RFC 9110): media-type rules apply after decoding.
		// Validating a still-encoded body would mis-classify legitimate gzip JSON-RPC
		// frames as invalid. Skip rather than introduce decompression here.
		return false
	}
	if !requestLooksLikeMCP(resp.Request) {
		// Narrow validation to traffic that carries an MCP streamable-HTTP signal,
		// so non-MCP application/json POSTs flowing through the catch-all are not
		// rewritten. Backward-compat clients omitting MCP-Protocol-Version on the
		// initial initialize will pass through unchanged.
		return false
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || mediaType == "application/json-rpc"
}

func hasIdentityContentEncoding(value string) bool {
	v := strings.TrimSpace(strings.ToLower(value))
	return v == "" || v == "identity"
}

func requestLooksLikeMCP(req *http.Request) bool {
	if req == nil {
		return false
	}
	if mcpparser.GetParsedMCPRequest(req.Context()) != nil {
		return true
	}
	return req.Header.Get("MCP-Protocol-Version") != "" || req.Header.Get("Mcp-Session-Id") != ""
}

func writeInvalidUpstreamJSONRPCResponse(resp *http.Response, validationErr error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    jsonRPCInvalidUpstreamCode,
			"message": "Invalid upstream JSON-RPC response",
			"data":    validationErr.Error(),
		},
		"id": nil,
	})
	if err != nil {
		body = []byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Invalid upstream JSON-RPC response"},"id":null}`)
	}

	resp.StatusCode = http.StatusBadGateway
	resp.Status = fmt.Sprintf("%d %s", http.StatusBadGateway, http.StatusText(http.StatusBadGateway))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))

	// Replace headers wholesale so upstream session/cookie/cache metadata is not
	// smuggled into the proxy-generated error. Only carry the fields needed to
	// describe this synthetic body.
	resp.Header = http.Header{}
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.Trailer = nil
}

// createResponseProcessor is a factory function that creates the appropriate
// response processor based on transport type.
func createResponseProcessor(
	transportType string,
	proxy *TransparentProxy,
	endpointPrefix string,
	trustProxyHeaders bool,
) ResponseProcessor {
	switch transportType {
	case types.TransportTypeSSE.String():
		return NewSSEResponseProcessor(proxy, endpointPrefix, trustProxyHeaders)
	case types.TransportTypeStreamableHTTP.String():
		return &NoOpResponseProcessor{}
	default:
		// Default to no-op for unknown transport types
		return &NoOpResponseProcessor{}
	}
}

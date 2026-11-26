// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp"
)

const (
	// maxUpstreamResponseSize bounds how much of an upstream application/json
	// response the proxy buffers to validate it. It matches maxResponseSize in
	// pkg/vmcp/client, which bounds vMCP's streamable-HTTP backend responses
	// for the same reason: a single response must not exhaust memory.
	maxUpstreamResponseSize = 100 * 1024 * 1024 // 100 MiB

	// invalidUpstreamResponseMessage is the fixed JSON-RPC error message sent
	// to the client in place of a rejected upstream response. It never
	// reflects upstream content.
	invalidUpstreamResponseMessage = "Invalid upstream JSON-RPC response"
)

// validateUpstreamResponse checks the upstream answer to an id-bearing POSTed
// JSON-RPC request before RoundTrip acts on it. Only a 200 application/json
// response is checked: it must carry a single structurally valid JSON-RPC
// response (see mcp.ValidateResponse). SSE streams, other statuses and other
// media types are not checked and are returned unchanged.
//
// It returns nil when resp may be forwarded; a checked body has then been
// buffered and resp.Body replaced with the identical bytes. Otherwise it logs
// the rejection once, releases the upstream body, and returns a synthetic
// 502 carrying a JSON-RPC error that echoes id.
//
// This is a best-effort structural check against a broken backend, not a
// security control: a well-formed response with malicious content passes.
func (t *tracingTransport) validateUpstreamResponse(
	req *http.Request, resp *http.Response, id jsonrpc2.ID, method string,
) *http.Response {
	if resp.StatusCode != http.StatusOK || responseMediaType(resp.Header) != "application/json" {
		return nil
	}
	reason := bufferAndValidateResponse(resp, maxUpstreamResponseSize)
	if reason == "" {
		return nil
	}
	slog.Warn("rejected invalid upstream JSON-RPC response",
		"reason", reason, "method", method, "target", t.p.targetURI)
	return invalidUpstreamResponse(req, id)
}

// responseMediaType returns the lowercased media type of the Content-Type
// header without parameters. Media type names are case-insensitive and
// parameters may be preceded by whitespace (RFC 9110 section 8.3.1); this
// parses as leniently as pkg/authz's response filter does.
func responseMediaType(h http.Header) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(h.Get("Content-Type"), ";")[0]))
}

// bufferAndValidateResponse reads resp.Body (at most limit bytes) and validates
// it as a JSON-RPC response. It returns "" on success, after replacing
// resp.Body with the unmodified bytes read and setting the content length to
// match. Otherwise it returns a fixed rejection reason, and resp.Body has been
// closed.
//
// A body still carrying a content coding is rejected without reading it:
// RoundTrip withholds the client's Accept-Encoding from id-bearing POSTs so
// Go's transport negotiates and decodes gzip itself, so an encoded body is one
// the backend compressed unprompted or the transport could not decode, and its
// bytes cannot be validated. This fails closed as pkg/authz's response filter
// does for the same case.
func bufferAndValidateResponse(resp *http.Response, limit int64) string {
	if mcp.HasContentEncoding(resp.Header) {
		discardBody(resp.Body, limit)
		return "content_encoding"
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		// Close without draining: the read already failed, or the body exceeds
		// the cap and draining it would read an unbounded stream to no purpose.
		_ = resp.Body.Close()
		if err != nil {
			return "read_error"
		}
		return "body_too_large"
	}
	// The body was read to EOF; a close error cannot affect the bytes in hand.
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	if err := mcp.ValidateResponse(body); err != nil {
		return err.Error()
	}
	return ""
}

// invalidUpstreamResponse builds the HTTP 502 returned in place of an upstream
// response that failed validation. It is modeled on
// mcp.ClassificationErrorResponse and plainResponse: fresh headers (no
// upstream Mcp-Session-Id, Set-Cookie or entity headers survive) and req
// attached as resp.Request.
//
// The JSON-RPC code is CodeInternalError (-32603). JSON-RPC defines no code for
// an upstream failure, and the codebase's precedent for one is -32603:
// upstreamErrorResponse and writeSSEErrorEvent in pkg/transport/proxy/streamable
// use it when the backend round trip fails, and mcp.CodeInternalError is
// documented for server-side failures not attributable to the request.
func invalidUpstreamResponse(req *http.Request, id jsonrpc2.ID) *http.Response {
	body := mcp.EncodeJSONRPCError(&jsonrpc2.Response{
		ID:    id,
		Error: jsonrpc2.NewError(mcp.CodeInternalError, invalidUpstreamResponseMessage),
	})
	hdr := make(http.Header)
	hdr.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode:    http.StatusBadGateway,
		Status:        fmt.Sprintf("%d %s", http.StatusBadGateway, http.StatusText(http.StatusBadGateway)),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        hdr,
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(bytes.NewReader(body)),
		Request:       req,
	}
}

// discardBody drains (at most limit bytes of) and closes an upstream body that
// will not be forwarded, so the connection can be reused without letting the
// backend hold the proxy on an unbounded stream.
func discardBody(body io.ReadCloser, limit int64) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, limit))
	_ = body.Close()
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// inboundRequestKey is the context key for storing the original inbound request.
// This is needed because httputil.ReverseProxy's ModifyResponse receives resp.Request
// as the *outbound* request, which has auto-injected X-Forwarded-* headers from
// SetXForwarded(). To read the client's original headers, we stash the inbound
// request in the outbound request's context during Rewrite.
type inboundRequestKey struct{}

// InboundRequestToContext returns a new context that carries the inbound request.
func InboundRequestToContext(ctx context.Context, req *http.Request) context.Context {
	return context.WithValue(ctx, inboundRequestKey{}, req)
}

// inboundRequestFromContext retrieves the inbound request from the context.
// Returns nil if not present.
func inboundRequestFromContext(ctx context.Context) *http.Request {
	req, _ := ctx.Value(inboundRequestKey{}).(*http.Request)
	return req
}

// sseRewriteConfig holds the configuration for rewriting SSE endpoint URLs.
// This is used to handle path-based ingress routing scenarios where the ingress
// strips a path prefix before forwarding to the backend MCP server.
type sseRewriteConfig struct {
	// prefix is the path prefix to prepend to endpoint URLs (e.g., "/playwright")
	prefix string
	// scheme is the URL scheme to use (e.g., "https"), empty means preserve original
	scheme string
	// host is the host to use (e.g., "public.example.com"), empty means preserve original
	host string
}

// hasRewriteConfig returns true if any rewriting is configured.
func (c sseRewriteConfig) hasRewriteConfig() bool {
	return c.prefix != "" || c.scheme != "" || c.host != ""
}

var sessionRe = regexp.MustCompile(`sessionId=([0-9A-Fa-f-]+)|"sessionId"\s*:\s*"([^"]+)"`)

// SSEResponseProcessor handles SSE-specific response processing including:
//   - Session ID extraction from SSE streams
//   - Endpoint URL rewriting for path-based routing
//   - Optional credential-shape redaction of tools/call results (see
//     WithSecretRedaction)
type SSEResponseProcessor struct {
	proxy             *TransparentProxy
	endpointPrefix    string
	trustProxyHeaders bool
	redactSecrets     bool
}

// NewSSEResponseProcessor creates a new SSE response processor.
func NewSSEResponseProcessor(
	proxy *TransparentProxy,
	endpointPrefix string,
	trustProxyHeaders bool,
	redactSecrets bool,
) *SSEResponseProcessor {
	return &SSEResponseProcessor{
		proxy:             proxy,
		endpointPrefix:    endpointPrefix,
		trustProxyHeaders: trustProxyHeaders,
		redactSecrets:     redactSecrets,
	}
}

// ShouldProcess returns true if the response is an SSE stream.
func (*SSEResponseProcessor) ShouldProcess(resp *http.Response) bool {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mediaType == "text/event-stream"
}

// ProcessResponse modifies SSE responses to:
// 1. Extract session IDs from endpoint events for session tracking
// 2. Rewrite endpoint URLs when X-Forwarded-Prefix or endpointPrefix is configured
//
// SSE Event Format:
//
//	event: endpoint
//	data: /sse?sessionId=abc
//
// Only "event: endpoint" events have their data field rewritten.
// Other events (e.g., "event: message") are passed through unchanged.
func (s *SSEResponseProcessor) ProcessResponse(resp *http.Response) error {
	if !s.ShouldProcess(resp) {
		return nil
	}

	// Get rewrite config from the request headers
	var rewriteConfig sseRewriteConfig
	if resp.Request != nil {
		rewriteConfig = s.getSSERewriteConfig(resp.Request)
	}

	pr, pw := io.Pipe()
	originalBody := resp.Body
	resp.Body = pr

	// NOTE: it would be better to have a proper function instead of a goroutine, as this
	// makes it harder to debug and test.
	go func() {
		defer func() {
			if err := pw.Close(); err != nil {
				slog.Debug("failed to close pipe writer", "error", err)
			}
		}()
		s.processSSEStream(originalBody, pw, rewriteConfig)
	}()

	return nil
}

// getSSERewriteConfig determines the SSE endpoint URL rewrite configuration based on priority:
// 1. Explicit endpointPrefix configuration (highest priority)
// 2. X-Forwarded-Prefix header (only when trustProxyHeaders is true)
// 3. No rewriting (default)
//
// IMPORTANT: req is the outbound request from httputil.ReverseProxy, which has
// auto-injected X-Forwarded-* headers via SetXForwarded(). To read the client's
// original headers we use the inbound request stashed in the context during Rewrite.
func (s *SSEResponseProcessor) getSSERewriteConfig(req *http.Request) sseRewriteConfig {
	config := sseRewriteConfig{}

	// Use the inbound (client-facing) request for reading forwarded headers,
	// because the outbound request has auto-injected X-Forwarded-* values
	// from httputil.ReverseProxy.SetXForwarded().
	inbound := inboundRequestFromContext(req.Context())
	if inbound == nil {
		// Fallback: if no inbound request in context (e.g. in tests), use
		// the outbound request directly.
		inbound = req
	}

	// Priority 1: Explicit endpointPrefix configuration
	if s.endpointPrefix != "" {
		config.prefix = s.endpointPrefix
	} else if s.trustProxyHeaders {
		// Priority 2: X-Forwarded-Prefix header from the original client request
		if prefix := inbound.Header.Get("X-Forwarded-Prefix"); prefix != "" {
			config.prefix = prefix
		}
	}

	// Also check for X-Forwarded-Proto and X-Forwarded-Host if trustProxyHeaders is enabled
	if s.trustProxyHeaders {
		if scheme := inbound.Header.Get("X-Forwarded-Proto"); scheme != "" {
			config.scheme = scheme
		}
		if host := inbound.Header.Get("X-Forwarded-Host"); host != "" {
			config.host = host
		}
	}

	return config
}

// rewriteEndpointURL rewrites an SSE endpoint URL with the given configuration.
// It handles both relative URLs (e.g., "/sse?sessionId=abc") and absolute URLs
// (e.g., "http://backend:8080/sse?sessionId=abc").
func rewriteEndpointURL(originalURL string, config sseRewriteConfig) (string, error) {
	if !config.hasRewriteConfig() {
		return originalURL, nil
	}

	parsed, err := url.Parse(originalURL)
	if err != nil {
		return originalURL, fmt.Errorf("failed to parse URL: %w", err)
	}

	// Prepend prefix to path
	if config.prefix != "" {
		// Ensure prefix starts with "/" and doesn't end with "/"
		prefix := config.prefix
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		prefix = strings.TrimSuffix(prefix, "/")
		parsed.Path = prefix + parsed.Path
	}

	// Override host if configured
	if config.host != "" {
		parsed.Host = config.host

		// Override scheme if configured
		if config.scheme != "" {
			parsed.Scheme = config.scheme
		}
	}

	return parsed.String(), nil
}

// sseLineProcessor handles line-by-line processing of SSE streams.
// It tracks event types and processes data lines for session extraction and URL rewriting.
type sseLineProcessor struct {
	proxy            *TransparentProxy
	rewriteConfig    sseRewriteConfig
	currentEventType string
	sessionFound     bool
	redactSecrets    bool

	// dataBuf accumulates consecutive raw "data:" lines belonging to the SAME
	// SSE event. Per the SSE spec, a compliant client concatenates them
	// (joined by "\n") into one logical value before consuming it -- URL
	// rewriting and redaction must operate on that same reassembled value, not
	// each "data:" line in isolation, or a hostile backend could split a
	// single JSON-RPC message across lines specifically to evade the
	// redaction scan while the real client still reassembles and sees it.
	dataBuf []string
}

// processLine processes a single SSE line and returns the lines to emit in
// its place: zero while a "data:" run is being buffered, or more than one
// when a buffered run is flushed ahead of a non-data line.
func (s *sseLineProcessor) processLine(line string) []string {
	// Parse SSE event type
	if strings.HasPrefix(line, "event:") {
		out := s.flushDataBuf()
		s.currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return append(out, line)
	}

	// Empty line marks the end of an SSE event, reset event type
	if line == "" {
		out := s.flushDataBuf()
		s.currentEventType = ""
		return append(out, line)
	}

	// Accumulate data lines; extraction/rewrite/redaction happens once the
	// run is flushed (see flushDataBuf), on the reassembled value.
	if strings.HasPrefix(line, "data:") {
		s.extractSessionID(line)
		s.dataBuf = append(s.dataBuf, line)
		return nil
	}

	out := s.flushDataBuf()
	return append(out, line)
}

// flushDataBuf reassembles any buffered "data:" lines into one logical value
// (per the SSE spec), applies endpoint-URL rewriting and/or secret redaction
// to that value, and returns the line(s) to emit. When neither transform
// changes anything, the original raw lines are returned unchanged so a
// disabled/no-op pass is byte-identical to the pre-buffering behavior.
func (s *sseLineProcessor) flushDataBuf() []string {
	if len(s.dataBuf) == 0 {
		return nil
	}
	rawLines := s.dataBuf
	s.dataBuf = nil

	value := joinSSEDataLines(rawLines)
	changed := false

	if s.currentEventType == "endpoint" && s.rewriteConfig.hasRewriteConfig() {
		rewritten, err := rewriteEndpointURL(value, s.rewriteConfig)
		switch {
		case err != nil:
			//nolint:gosec // G706: logging endpoint URL from SSE stream
			slog.Warn("failed to rewrite endpoint URL", "url", value, "error", err)
		case rewritten != value:
			//nolint:gosec // G706: logging endpoint URLs from SSE stream
			slog.Debug("rewrote SSE endpoint URL", "from", value, "to", rewritten)
			value = rewritten
			changed = true
		}
	}

	if s.redactSecrets {
		if redacted, ok, err := redactJSONRPCBody([]byte(value)); err == nil && ok {
			value = string(redacted)
			changed = true
		}
	}

	if !changed {
		return rawLines
	}
	return []string{"data: " + value}
}

// extractSessionID extracts and stores the session ID from a data line.
func (s *sseLineProcessor) extractSessionID(line string) {
	if s.sessionFound {
		return
	}
	if m := sessionRe.FindStringSubmatch(line); m != nil {
		sid := m[1]
		if sid == "" {
			sid = m[2]
		}
		s.proxy.setServerInitialized()
		if err := s.proxy.sessionManager.AddWithID(normalizeSessionID(sid)); err != nil {
			slog.Error("failed to create session from SSE line", "error", err)
		}
		s.sessionFound = true
	}
}

// processSSEStream processes an SSE stream, extracting session IDs and rewriting URLs.
func (s *SSEResponseProcessor) processSSEStream(originalBody io.Reader, pw *io.PipeWriter, rewriteConfig sseRewriteConfig) {
	scanner := bufio.NewScanner(originalBody)
	// NOTE: The following line mitigates the issue of the response body being too large.
	// By default, the maximum token size of the scanner is 64KB, which is too small in
	// the case of e.g. images. This raises the limit to 1MB. This is a workaround, and
	// not a proper fix.
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024*1)

	processor := &sseLineProcessor{
		proxy:         s.proxy,
		rewriteConfig: rewriteConfig,
		redactSecrets: s.redactSecrets,
	}

	for scanner.Scan() {
		for _, line := range processor.processLine(scanner.Text()) {
			if _, err := pw.Write([]byte(line + "\n")); err != nil {
				return
			}
		}
	}
	// A stream that ends without a trailing blank line still has a pending
	// event to reassemble and forward.
	for _, line := range processor.flushDataBuf() {
		if _, err := pw.Write([]byte(line + "\n")); err != nil {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Error("failed to scan response body", "error", err)
	}

	if readCloser, ok := originalBody.(io.ReadCloser); ok {
		if _, err := io.Copy(pw, readCloser); err != nil && err != io.EOF {
			slog.Error("failed to copy response body", "error", err)
		}
	}
}

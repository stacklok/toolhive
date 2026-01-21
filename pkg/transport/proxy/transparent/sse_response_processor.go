// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bufio"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
)

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
// - Session ID extraction from SSE streams
// - Endpoint URL rewriting for path-based routing
type SSEResponseProcessor struct {
	proxy             *TransparentProxy
	endpointPrefix    string
	trustProxyHeaders bool
}

// NewSSEResponseProcessor creates a new SSE response processor.
func NewSSEResponseProcessor(
	proxy *TransparentProxy,
	endpointPrefix string,
	trustProxyHeaders bool,
) *SSEResponseProcessor {
	return &SSEResponseProcessor{
		proxy:             proxy,
		endpointPrefix:    endpointPrefix,
		trustProxyHeaders: trustProxyHeaders,
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
				logger.Debugf("Failed to close pipe writer: %v", err)
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
func (s *SSEResponseProcessor) getSSERewriteConfig(req *http.Request) sseRewriteConfig {
	config := sseRewriteConfig{}

	// Priority 1: Explicit endpointPrefix configuration
	if s.endpointPrefix != "" {
		config.prefix = s.endpointPrefix
	} else if s.trustProxyHeaders {
		// Priority 2: X-Forwarded-Prefix header
		if prefix := req.Header.Get("X-Forwarded-Prefix"); prefix != "" {
			config.prefix = prefix
		}
	}

	// Also check for X-Forwarded-Proto and X-Forwarded-Host if trustProxyHeaders is enabled
	if s.trustProxyHeaders {
		if scheme := req.Header.Get("X-Forwarded-Proto"); scheme != "" {
			config.scheme = scheme
		}
		if host := req.Header.Get("X-Forwarded-Host"); host != "" {
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

	// Override scheme if configured
	if config.scheme != "" {
		parsed.Scheme = config.scheme
	}

	// Override host if configured
	if config.host != "" {
		parsed.Host = config.host
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
}

// processLine processes a single SSE line and returns the potentially modified line.
func (s *sseLineProcessor) processLine(line string) string {
	// Parse SSE event type
	if strings.HasPrefix(line, "event:") {
		s.currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return line
	}

	// Empty line marks the end of an SSE event, reset event type
	if line == "" {
		s.currentEventType = ""
		return line
	}

	// Process data lines
	if strings.HasPrefix(line, "data:") {
		return s.processDataLine(line)
	}

	return line
}

// processDataLine handles SSE data lines for session extraction and URL rewriting.
func (s *sseLineProcessor) processDataLine(line string) string {
	dataContent := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

	// Extract session ID for tracking (from any data line)
	s.extractSessionID(line)

	// Rewrite endpoint URLs only for "endpoint" events
	if s.currentEventType == "endpoint" && s.rewriteConfig.hasRewriteConfig() {
		return s.rewriteDataLine(line, dataContent)
	}

	return line
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
		if err := s.proxy.sessionManager.AddWithID(sid); err != nil {
			logger.Errorf("Failed to create session from SSE line: %v", err)
		}
		s.sessionFound = true
	}
}

// rewriteDataLine rewrites the URL in an endpoint event's data line.
func (s *sseLineProcessor) rewriteDataLine(line, dataContent string) string {
	rewrittenURL, err := rewriteEndpointURL(dataContent, s.rewriteConfig)
	if err != nil {
		logger.Warnf("Failed to rewrite endpoint URL %q: %v", dataContent, err)
		return line
	}
	if rewrittenURL != dataContent {
		logger.Debugf("Rewrote SSE endpoint URL from %q to %q", dataContent, rewrittenURL)
		return "data: " + rewrittenURL
	}
	return line
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
	}

	for scanner.Scan() {
		line := processor.processLine(scanner.Text())
		if _, err := pw.Write([]byte(line + "\n")); err != nil {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Errorf("Failed to scan response body: %v", err)
	}

	if readCloser, ok := originalBody.(io.ReadCloser); ok {
		if _, err := io.Copy(pw, readCloser); err != nil && err != io.EOF {
			logger.Errorf("Failed to copy response body: %v", err)
		}
	}
}

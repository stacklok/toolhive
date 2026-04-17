// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package transparent provides MCP ping functionality for transparent proxies.
package transparent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/healthcheck"
)

// MCPPinger implements healthcheck.MCPPinger for transparent proxies
type MCPPinger struct {
	targetURL string
	client    *http.Client
}

const (
	// DefaultPingerTimeout is the default timeout for health check pings
	DefaultPingerTimeout = 5 * time.Second
)

// NewMCPPinger creates a new MCP pinger for transparent proxies
func NewMCPPinger(targetURL string) healthcheck.MCPPinger {
	return NewMCPPingerWithTimeout(targetURL, DefaultPingerTimeout)
}

// NewMCPPingerWithTimeout creates a new MCP pinger with a custom timeout
func NewMCPPingerWithTimeout(targetURL string, timeout time.Duration) healthcheck.MCPPinger {
	if timeout <= 0 {
		timeout = DefaultPingerTimeout
	}
	return &MCPPinger{
		targetURL: targetURL,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Ping performs a simple HTTP health check for SSE transport servers
// For SSE transport, we don't send MCP ping requests because that would require
// establishing an SSE session first. Instead, we do a simple HTTP GET to check
// if the server is responding.
func (p *MCPPinger) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()

	// Create a simple GET request to check if the server is responding
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.targetURL, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	//nolint:gosec // G706: logging target URL from config
	slog.Debug("checking SSE server health", "target", p.targetURL)

	// Send the request
	resp, err := p.client.Do(req) // #nosec G704 -- targetURL is the local MCP server health endpoint
	if err != nil {
		return time.Since(start), fmt.Errorf("failed to connect to SSE server: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("failed to close response body", "error", err)
		}
	}()

	duration := time.Since(start)

	// For Streamable HTTP servers, we expect various responses:
	// - 200 OK for root endpoints
	// - 404 for non-existent endpoints (but server is still alive)
	// - 401 for remote workloads (we may not be able to authenticate, but this response indicates the server is alive).
	// - Other 4xx/5xx may indicate server issues
	// For now, we accept any non 50x response for both local and remote.
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		//nolint:gosec // G706: logging HTTP status code from health check response
		slog.Debug("sse server health check successful",
			"duration", duration, "status", resp.StatusCode)
		return duration, nil
	}

	return duration, fmt.Errorf("SSE server health check failed with status %d", resp.StatusCode)
}

// StatelessMCPPinger health-checks stateless streamable-HTTP servers via POST ping.
// Stateless servers don't support GET, so we send a minimal JSON-RPC ping instead.
type StatelessMCPPinger struct {
	targetURL string
	client    *http.Client
}

// NewStatelessMCPPinger creates a pinger for stateless streamable-HTTP servers.
func NewStatelessMCPPinger(targetURL string) healthcheck.MCPPinger {
	return NewStatelessMCPPingerWithTimeout(targetURL, DefaultPingerTimeout)
}

// NewStatelessMCPPingerWithTimeout creates a stateless pinger with a custom timeout.
func NewStatelessMCPPingerWithTimeout(targetURL string, timeout time.Duration) healthcheck.MCPPinger {
	if timeout <= 0 {
		timeout = DefaultPingerTimeout
	}
	return &StatelessMCPPinger{
		targetURL: targetURL,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Ping sends a JSON-RPC ping POST to check if the stateless server is reachable.
// Accepts any 2xx-4xx response as healthy; only network errors and 5xx indicate failure.
func (p *StatelessMCPPinger) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()

	body := `{"jsonrpc":"2.0","id":0,"method":"ping","params":{}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.targetURL, strings.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("failed to create stateless ping request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	//nolint:gosec // G706: logging target URL from config
	slog.Debug("checking stateless MCP server health via POST ping", "target", p.targetURL)

	resp, err := p.client.Do(req)
	if err != nil {
		return time.Since(start), fmt.Errorf("stateless ping failed to connect: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("failed to close ping response body", "error", err)
		}
	}()

	duration := time.Since(start)

	// Accept 2xx-4xx: even 401/403 means the server is reachable.
	// Only 5xx or network errors indicate the server is down.
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		//nolint:gosec // G706: logging HTTP status code from health check response
		slog.Debug("stateless MCP server health check successful",
			"duration", duration, "status", resp.StatusCode)
		return duration, nil
	}

	return duration, fmt.Errorf("stateless ping returned status %d", resp.StatusCode)
}

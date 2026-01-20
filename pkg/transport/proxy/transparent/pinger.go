// Package transparent provides MCP ping functionality for transparent proxies.
package transparent

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/logger"
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

	logger.Debugf("Checking SSE server health at %s", p.targetURL)

	// Send the request
	resp, err := p.client.Do(req)
	if err != nil {
		return time.Since(start), fmt.Errorf("failed to connect to SSE server: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Debugf("Failed to close response body: %v", err)
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
		logger.Debugf("SSE server health check successful in %v (status: %d)", duration, resp.StatusCode)
		return duration, nil
	}

	return duration, fmt.Errorf("SSE server health check failed with status %d", resp.StatusCode)
}

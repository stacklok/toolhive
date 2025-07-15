// Package httpsse provides MCP ping functionality for HTTP SSE proxies.
package httpsse

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/logger"
)

// MCPPinger implements healthcheck.MCPPinger for HTTP SSE proxies
type MCPPinger struct {
	proxy *HTTPSSEProxy
}

// NewMCPPinger creates a new MCP pinger for HTTP SSE proxies
func NewMCPPinger(proxy *HTTPSSEProxy) healthcheck.MCPPinger {
	return &MCPPinger{
		proxy: proxy,
	}
}

// Ping sends a JSON-RPC ping request to the MCP server and waits for the response
// Following the MCP ping specification:
// https://modelcontextprotocol.io/specification/2025-03-26/basic/utilities/ping
func (p *MCPPinger) Ping(ctx context.Context) (time.Duration, error) {
	if p.proxy == nil {
		return 0, fmt.Errorf("proxy not available")
	}
	messageCh := p.proxy.GetMessageChannel()
	if messageCh == nil {
		return 0, fmt.Errorf("message channel not available")
	}

	// 1️⃣ Send initialize
	initReq, err := jsonrpc2.NewCall(
		jsonrpc2.StringID(fmt.Sprintf("init_%d", time.Now().UnixNano())),
		"initialize",
		json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"toolhive-health","version":"1.0"}}`),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create initialize request: %w", err)
	}
	select {
	case messageCh <- initReq:
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// 2️⃣ Send initialized notification
	initNotif, err := jsonrpc2.NewNotification("notifications/initialized", nil)
	if err != nil {
		return 0, fmt.Errorf("failed to create initialized notification: %w", err)
	}
	select {
	case messageCh <- initNotif:
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// 3️⃣ Wait for 2 seconds to allow server init
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// 4️⃣ Send ping request (same as before)
	pingID := fmt.Sprintf("ping_%d", time.Now().UnixNano())
	pingRequest, err := jsonrpc2.NewCall(jsonrpc2.StringID(pingID), "ping", json.RawMessage("{}"))
	if err != nil {
		return 0, fmt.Errorf("failed to create ping request: %w", err)
	}

	start := time.Now()
	select {
	case messageCh <- pingRequest:
		logger.Debugf("Sent MCP ping request with ID: %s", pingID)
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	// 5️⃣ Wait for ping response with timeout
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case raw := <-messageCh:
			if resp, ok := raw.(*jsonrpc2.Response); ok && resp.ID == pingRequest.ID {
				if resp.Error != nil {
					return 0, fmt.Errorf("ping failed: %v", resp.Error)
				}
				duration := time.Since(start)
				logger.Debugf("MCP ping completed in %v", duration)
				return duration, nil
			}
		case <-timer.C:
			return 0, fmt.Errorf("timeout waiting for ping response")
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

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
	if err := p.sendInitialize(ctx); err != nil {
		return 0, err
	}

	if err := wait(ctx, 2*time.Second); err != nil {
		return 0, err
	}

	return p.sendPing(ctx)
}

// sendInitialize handles the initialize + initialized steps
func (p *MCPPinger) sendInitialize(ctx context.Context) error {
	if p.proxy == nil {
		return fmt.Errorf("proxy not available")
	}
	msgCh := p.proxy.GetMessageChannel()
	if msgCh == nil {
		return fmt.Errorf("message channel not available")
	}

	// Send initialize
	initID := fmt.Sprintf("init_%d", time.Now().UnixNano())
	initReq, err := jsonrpc2.NewCall(jsonrpc2.StringID(initID), "initialize",
		json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"toolhive-health","version":"1.0"}}`))
	if err != nil {
		return fmt.Errorf("create initialize request: %w", err)
	}
	select {
	case msgCh <- initReq:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Send initialized notification
	initNotif, err := jsonrpc2.NewNotification("notifications/initialized", nil)
	if err != nil {
		return fmt.Errorf("create initialized notification: %w", err)
	}
	select {
	case msgCh <- initNotif:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// wait pauses for a duration or returns an error if context is done
func wait(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendPing executes the ping request and waits for a response
func (p *MCPPinger) sendPing(ctx context.Context) (time.Duration, error) {
	msgCh := p.proxy.GetMessageChannel()

	pingID := fmt.Sprintf("ping_%d", time.Now().UnixNano())
	pingReq, err := jsonrpc2.NewCall(jsonrpc2.StringID(pingID), "ping", json.RawMessage(`{}`))
	if err != nil {
		return 0, fmt.Errorf("create ping request: %w", err)
	}

	start := time.Now()
	select {
	case msgCh <- pingReq:
		logger.Debugf("Sent ping ID %s", pingID)
	case <-ctx.Done():
		return 0, ctx.Err()
	}

	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case raw := <-msgCh:
			if resp, ok := raw.(*jsonrpc2.Response); ok && resp.ID == pingReq.ID {
				if resp.Error != nil {
					return 0, fmt.Errorf("ping failed: %v", resp.Error)
				}
				return time.Since(start), nil
			}
		case <-timer.C:
			return 0, fmt.Errorf("timeout waiting for ping")
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

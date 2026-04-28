// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/core"
	thclient "github.com/stacklok/toolhive/pkg/mcp/client"
)

var errStdioToolsNotAvailable = errors.New("tool listing not available for STDIO servers")

// workloadTransport returns the transport string to use for the given workload.
// It prefers ProxyMode (set for stdio transports) and falls back to TransportType.
func workloadTransport(w *core.Workload) string {
	if w.ProxyMode != "" {
		return w.ProxyMode
	}
	return string(w.TransportType)
}

// startMCPClientConnect returns a tea.Cmd that creates and connects an MCP
// client asynchronously, keeping the Update goroutine non-blocking.
func startMCPClientConnect(ctx context.Context, w *core.Workload) tea.Cmd {
	name := w.Name
	serverURL := w.URL
	transport := workloadTransport(w)
	return func() tea.Msg {
		c, err := thclient.Connect(ctx, serverURL, transport, "toolhive-tui")
		if err != nil {
			return mcpClientReadyMsg{workloadName: name, err: err}
		}
		return mcpClientReadyMsg{workloadName: name, client: c}
	}
}

// fetchTools queries the MCP server for its tool list via an already-connected client.
func fetchTools(ctx context.Context, c *mcpclient.Client) ([]mcp.Tool, error) {
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

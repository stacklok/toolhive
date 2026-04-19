// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/core"
)

var errStdioToolsNotAvailable = errors.New("tool listing not available for STDIO servers")

// createMCPClient creates a new MCP SDK client for the given workload.
// The client is not yet started or initialized; call connectMCPClient next.
func createMCPClient(workload *core.Workload) (*mcpclient.Client, error) {
	transportType := workload.ProxyMode
	if transportType == "" {
		transportType = string(workload.TransportType)
	}
	switch transportType {
	case "streamable-http":
		return mcpclient.NewStreamableHttpClient(workload.URL)
	case "sse":
		return mcpclient.NewSSEMCPClient(workload.URL)
	default:
		return nil, fmt.Errorf("unsupported transport type %q for TUI client", transportType)
	}
}

// connectMCPClient starts the client transport and performs the MCP initialize handshake.
func connectMCPClient(ctx context.Context, c *mcpclient.Client) error {
	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("start MCP client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "toolhive-tui",
		Version: "1.0.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("initialize MCP client: %w", err)
	}
	return nil
}

// fetchTools queries the MCP server for its tool list via an already-connected client.
func fetchTools(ctx context.Context, c *mcpclient.Client) ([]mcp.Tool, error) {
	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

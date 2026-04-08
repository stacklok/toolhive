// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauthfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
)

// errStdioToolsNotAvailable is returned when tool listing is attempted for a STDIO server.
// STDIO servers only support a single MCP initialize handshake; calling initialize again
// from the TUI would interfere with the real client connection.
var errStdioToolsNotAvailable = errors.New("tool listing not available for STDIO servers")

// newBackendClientAndTarget creates an authenticated MCP backend client and a
// BackendTarget for the given workload. This is the common setup shared by
// fetchTools and callTool.
func newBackendClientAndTarget(ctx context.Context, workload *core.Workload) (vmcp.BackendClient, *vmcp.BackendTarget, error) {
	registry, err := vmcpauthfactory.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	if err != nil {
		return nil, nil, err
	}

	mcpClient, err := vmcpclient.NewHTTPBackendClient(registry)
	if err != nil {
		return nil, nil, err
	}

	// For stdio workloads the proxy exposes an HTTP transport (sse or streamable-http).
	// ProxyMode holds the actual transport type clients should use.
	transportType := workload.ProxyMode
	if transportType == "" {
		transportType = string(workload.TransportType)
	}

	target := &vmcp.BackendTarget{
		WorkloadID:    workload.Name,
		WorkloadName:  workload.Name,
		BaseURL:       workload.URL,
		TransportType: transportType,
	}

	return mcpClient, target, nil
}

// fetchTools connects to the running MCP server and returns its tool list.
func fetchTools(ctx context.Context, workload *core.Workload) ([]vmcp.Tool, error) {
	mcpClient, target, err := newBackendClientAndTarget(ctx, workload)
	if err != nil {
		return nil, err
	}

	caps, err := mcpClient.ListCapabilities(ctx, target)
	if err != nil {
		return nil, err
	}

	return caps.Tools, nil
}

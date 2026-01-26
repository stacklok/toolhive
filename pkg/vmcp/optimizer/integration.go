// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"

	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

// Integration is the interface for optimizer functionality in vMCP.
// This interface encapsulates all optimizer logic, keeping server.go clean.
type Integration interface {
	// Initialize performs all optimizer initialization:
	//   - Registers optimizer tools globally with the MCP server
	//   - Ingests initial backends from the registry
	// This should be called once during server startup, after the MCP server is created.
	Initialize(ctx context.Context, mcpServer *server.MCPServer, backendRegistry vmcp.BackendRegistry) error

	// HandleSessionRegistration handles session registration for optimizer mode.
	// Returns true if optimizer mode is enabled and handled the registration,
	// false if optimizer is disabled and normal registration should proceed.
	// The resourceConverter function converts vmcp.Resource to server.ServerResource.
	HandleSessionRegistration(
		ctx context.Context,
		sessionID string,
		caps *aggregator.AggregatedCapabilities,
		mcpServer *server.MCPServer,
		resourceConverter func([]vmcp.Resource) []server.ServerResource,
	) (bool, error)

	// Close cleans up optimizer resources
	Close() error

	// OptimizerHandlerProvider is embedded to provide tool handlers
	adapter.OptimizerHandlerProvider
}

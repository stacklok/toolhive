// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"context"
)

// MCPFeature represents an MCP feature type.
// In the MCP protocol, there are three main features:
// - Tools: Allow models to call functions in external systems
// - Prompts: Provide structured templates for interacting with language models
// - Resources: Share data that provides context to language models
type MCPFeature string

const (
	// MCPFeatureTool represents the MCP tool feature.
	MCPFeatureTool MCPFeature = "tool"
	// MCPFeaturePrompt represents the MCP prompt feature.
	MCPFeaturePrompt MCPFeature = "prompt"
	// MCPFeatureResource represents the MCP resource feature.
	MCPFeatureResource MCPFeature = "resource"
)

// MCPOperation represents an operation on an MCP feature.
// Each feature supports different operations:
// - List: Get a list of available items (tools, prompts, resources)
// - Get: Get a specific prompt
// - Call: Call a specific tool
// - Read: Read a specific resource
type MCPOperation string

const (
	// MCPOperationList represents a list operation.
	MCPOperationList MCPOperation = "list"
	// MCPOperationGet represents a get operation.
	MCPOperationGet MCPOperation = "get"
	// MCPOperationCall represents a call operation.
	MCPOperationCall MCPOperation = "call"
	// MCPOperationRead represents a read operation.
	MCPOperationRead MCPOperation = "read"
)

// Authorizer defines the interface for making authorization decisions.
// Implementations of this interface evaluate whether a given operation on an MCP feature
// should be permitted, based on JWT claims and the specific resource being accessed.
type Authorizer interface {
	AuthorizeWithJWTClaims(
		ctx context.Context,
		feature MCPFeature,
		operation MCPOperation,
		resourceID string,
		arguments map[string]interface{},
	) (bool, error)
}

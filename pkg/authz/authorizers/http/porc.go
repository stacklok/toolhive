// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// PORC represents a Principal-Operation-Resource-Context authorization request.
// This is a common input format for Policy Decision Points (PDPs).
type PORC map[string]interface{}

// Principal represents the principal (subject) making the request.
type Principal map[string]interface{}

// Context represents additional context for the authorization decision.
type Context map[string]interface{}

// PORCBuilder builds PORC (Principal-Operation-Resource-Context) expressions
// from MCP authorization parameters for use with HTTP-based PDPs.
type PORCBuilder struct {
	serverID      string
	contextConfig ContextConfig
	claimMapper   ClaimMapper
}

// NewPORCBuilder creates a new PORC builder with the given server ID, context configuration,
// and claim mapper.
func NewPORCBuilder(serverID string, contextConfig ContextConfig, claimMapper ClaimMapper) *PORCBuilder {
	return &PORCBuilder{
		serverID:      serverID,
		contextConfig: contextConfig,
		claimMapper:   claimMapper,
	}
}

// Build creates a PORC expression from MCP authorization parameters.
// It maps MCP concepts to the PORC model:
//   - principal: Built from JWT claims (sub, roles, groups, scopes, etc.)
//   - operation: Derived from MCP feature and operation (e.g., "mcp:tool:call")
//   - resource: The MCP resource identifier (tool name, prompt name, resource URI)
//   - context: Additional context including tool arguments
func (b *PORCBuilder) Build(
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	claims map[string]interface{},
	arguments map[string]interface{},
) PORC {
	porc := make(PORC)

	// Build principal from JWT claims
	porc["principal"] = b.buildPrincipal(claims)

	// Build operation string from MCP feature and operation
	porc["operation"] = b.buildOperation(feature, operation)

	// Set resource identifier
	porc["resource"] = b.buildResource(feature, resourceID)

	// Build context from arguments and additional metadata
	porc["context"] = b.buildContext(feature, operation, resourceID, arguments)

	return porc
}

// buildPORC is a test helper that creates a PORC with all context options enabled.
// This function is only intended for use in tests within this package.
func buildPORC(
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	claims map[string]interface{},
	arguments map[string]interface{},
) PORC {
	// Use a config with all context enabled and MPE claim mapper for testing
	contextConfig := ContextConfig{
		IncludeArgs:      true,
		IncludeOperation: true,
	}
	claimMapper := &MPEClaimMapper{}
	return NewPORCBuilder("test", contextConfig, claimMapper).Build(feature, operation, resourceID, claims, arguments)
}

// buildPrincipal constructs the principal object from JWT claims using the configured ClaimMapper.
// The claim mapping is delegated to the ClaimMapper, which can be configured to use different
// conventions (e.g., MPE-specific m-prefixed claims or standard OIDC claims).
//
// Note: Returns map[string]interface{} (the concrete type) rather than the Principal type alias
// for clarity and to match the ClaimMapper interface. Both types are equivalent for JSON
// marshaling, but using the concrete type avoids unnecessary type assertions and makes the
// return value explicit.
func (b *PORCBuilder) buildPrincipal(claims map[string]interface{}) map[string]interface{} {
	return b.claimMapper.MapClaims(claims)
}

// buildOperation constructs the operation string from MCP feature and operation.
// Format: "mcp:<feature>:<operation>"
// Examples:
//   - "mcp:tool:call" for calling a tool
//   - "mcp:prompt:get" for getting a prompt
//   - "mcp:resource:read" for reading a resource
//   - "mcp:tool:list" for listing tools
func (*PORCBuilder) buildOperation(feature authorizers.MCPFeature, operation authorizers.MCPOperation) string {
	return fmt.Sprintf("mcp:%s:%s", feature, operation)
}

// buildResource constructs the resource identifier.
// Format: "mrn:mcp:<serverID>:<feature>:<resourceID>"
// Examples:
//   - "mrn:mcp:myserver:tool:weather" for the weather tool
//   - "mrn:mcp:myserver:prompt:greeting" for the greeting prompt
//   - "mrn:mcp:myserver:resource:file://data.json" for a resource
func (b *PORCBuilder) buildResource(feature authorizers.MCPFeature, resourceID string) string {
	return fmt.Sprintf("mrn:mcp:%s:%s:%s", b.serverID, feature, resourceID)
}

// buildContext constructs the context object with additional metadata.
// Note: Returns map[string]interface{} (the concrete type) rather than the Context type alias
// for clarity. Both types are equivalent for JSON marshaling, but using the concrete type
// makes the return value explicit and avoids unnecessary type assertions.
//
// The context may include an "mcp" object with the following fields, depending
// on the ContextConfig settings:
//   - feature: The MCP feature type (tool, prompt, resource) - if IncludeOperation is true
//   - operation: The MCP operation (call, get, read, list) - if IncludeOperation is true
//   - resource_id: The resource identifier - if IncludeOperation is true
//   - args: Tool/prompt arguments (if present) - if IncludeArgs is true
//
// Important: The "mcp" object is only included in the context if it would contain
// at least one field. This means:
//   - If both IncludeOperation and IncludeArgs are false, returns an empty context {}
//   - If only IncludeArgs is true but arguments is nil/empty, returns an empty context {}
//   - If only IncludeOperation is true, returns context with mcp object containing operation fields
//   - If both are true and arguments exist, returns context with all enabled fields
//
// This prevents empty mcp objects from being included in the PORC, keeping it minimal.
func (b *PORCBuilder) buildContext(
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) map[string]interface{} {
	ctx := make(map[string]interface{})

	// Only build the mcp object if at least one context option is enabled
	if !b.contextConfig.IncludeArgs && !b.contextConfig.IncludeOperation {
		return ctx
	}

	// Build nested MCP object with metadata based on configuration
	mcp := make(map[string]interface{})

	// Include operation metadata if enabled
	if b.contextConfig.IncludeOperation {
		mcp["feature"] = string(feature)
		mcp["operation"] = string(operation)
		mcp["resource_id"] = resourceID
	}

	// Include tool/prompt arguments if enabled and present
	if b.contextConfig.IncludeArgs && len(arguments) > 0 {
		mcp["args"] = arguments
	}

	// Only add mcp to context if it has any fields
	if len(mcp) > 0 {
		ctx["mcp"] = mcp
	}

	return ctx
}

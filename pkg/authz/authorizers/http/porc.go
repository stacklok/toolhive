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
	serverId      string
	contextConfig ContextConfig
}

// NewPORCBuilder creates a new PORC builder with the given server ID and context configuration.
func NewPORCBuilder(serverId string, contextConfig ContextConfig) *PORCBuilder {
	return &PORCBuilder{
		serverId:      serverId,
		contextConfig: contextConfig,
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

// BuildPORC is a wrapper for PORCBuilder.Build() with all context enabled (for testing).
func BuildPORC(
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	claims map[string]interface{},
	arguments map[string]interface{},
) PORC {
	// Use a config with all context enabled for backward compatibility in tests
	contextConfig := ContextConfig{
		IncludeArgs:      true,
		IncludeOperation: true,
	}
	return NewPORCBuilder("test", contextConfig).Build(feature, operation, resourceID, claims, arguments)
}

// buildPrincipal constructs the principal object from JWT claims.
// It maps standard JWT claims to principal attributes:
//   - sub -> sub (subject identifier)
//   - roles/mroles -> mroles (roles)
//   - groups/mgroups -> mgroups (groups)
//   - scope/scopes -> scopes (access scopes)
//   - clearance/mclearance -> mclearance (clearance level)
//
// Note: Returns map[string]interface{} (not Principal type alias) to ensure
// the PDP can properly unmarshal the PORC structure for identity phase evaluation.
func (*PORCBuilder) buildPrincipal(claims map[string]interface{}) map[string]interface{} {
	principal := make(map[string]interface{})

	if claims == nil {
		return principal
	}

	// Map standard JWT claims
	if sub, ok := claims["sub"]; ok {
		principal["sub"] = sub
	}

	// Map roles (check both 'roles' and 'mroles')
	if roles, ok := claims["mroles"]; ok {
		principal["mroles"] = roles
	} else if roles, ok := claims["roles"]; ok {
		principal["mroles"] = roles
	}

	// Map groups (check both 'groups' and 'mgroups')
	if groups, ok := claims["mgroups"]; ok {
		principal["mgroups"] = groups
	} else if groups, ok := claims["groups"]; ok {
		principal["mgroups"] = groups
	}

	// Map scopes (check both 'scope' and 'scopes')
	if scopes, ok := claims["scopes"]; ok {
		principal["scopes"] = scopes
	} else if scope, ok := claims["scope"]; ok {
		principal["scopes"] = scope
	}

	// Map clearance level
	if clearance, ok := claims["mclearance"]; ok {
		principal["mclearance"] = clearance
	} else if clearance, ok := claims["clearance"]; ok {
		principal["mclearance"] = clearance
	}

	// Map annotations (initialize empty if not present for identity phase)
	if annotations, ok := claims["mannotations"]; ok {
		principal["mannotations"] = annotations
	} else if annotations, ok := claims["annotations"]; ok {
		principal["mannotations"] = annotations
	} else {
		// Some PDPs require mannotations to be present for identity phase evaluation
		principal["mannotations"] = make(map[string]interface{})
	}

	return principal
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
// Format: "mrn:mcp:<serverId>:<feature>:<resourceID>"
// Examples:
//   - "mrn:mcp:myserver:tool:weather" for the weather tool
//   - "mrn:mcp:myserver:prompt:greeting" for the greeting prompt
//   - "mrn:mcp:myserver:resource:file://data.json" for a resource
func (b *PORCBuilder) buildResource(feature authorizers.MCPFeature, resourceID string) string {
	return fmt.Sprintf("mrn:mcp:%s:%s:%s", b.serverId, feature, resourceID)
}

// buildContext constructs the context object with additional metadata.
// Note: Returns map[string]interface{} (not Context type alias) to ensure
// the PDP can properly unmarshal the PORC structure.
//
// The context may include an "mcp" object with the following fields, depending
// on the ContextConfig settings:
//   - feature: The MCP feature type (tool, prompt, resource) - if IncludeOperation is true
//   - operation: The MCP operation (call, get, read, list) - if IncludeOperation is true
//   - resource_id: The resource identifier - if IncludeOperation is true
//   - args: Tool/prompt arguments (if present) - if IncludeArgs is true
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

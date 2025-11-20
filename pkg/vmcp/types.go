package vmcp

import "context"

// This file contains shared domain types used across multiple vmcp subpackages.
// Following DDD principles, these are core domain concepts that cross bounded contexts.

// BackendTarget identifies a specific backend workload and provides
// the information needed to forward requests to it.
type BackendTarget struct {
	// WorkloadID is the unique identifier for the backend workload.
	WorkloadID string

	// WorkloadName is the human-readable name of the workload.
	WorkloadName string

	// BaseURL is the base URL for the backend's MCP server.
	// For local deployments: http://localhost:PORT
	// For Kubernetes: http://service-name.namespace.svc.cluster.local:PORT
	BaseURL string

	// TransportType specifies the MCP transport protocol.
	// Supported: "stdio", "http", "sse", "streamable-http"
	TransportType string

	// OriginalCapabilityName is the original name of the capability (tool/resource/prompt)
	// as known by the backend. This is used when forwarding requests to the backend.
	//
	// When conflict resolution renames capabilities, this field preserves the original name:
	// - Prefix strategy: "fetch" → "fetch_fetch" (OriginalCapabilityName="fetch")
	// - Priority strategy: usually unchanged (OriginalCapabilityName="tool_name")
	// - Manual strategy: "fetch" → "custom_name" (OriginalCapabilityName="fetch")
	//
	// If empty, the resolved name is used when forwarding to the backend.
	//
	// IMPORTANT: Do NOT access this field directly when forwarding requests to backends.
	// Use GetBackendCapabilityName() method instead, which handles both renamed and
	// non-renamed capabilities correctly. Direct access can lead to incorrect behavior
	// when capabilities are not renamed (OriginalCapabilityName will be empty).
	//
	// Example (WRONG):
	//   client.CallTool(ctx, target, target.OriginalCapabilityName, args) // BUG: fails when empty
	//
	// Example (CORRECT):
	//   client.CallTool(ctx, target, target.GetBackendCapabilityName(toolName), args)
	OriginalCapabilityName string

	// AuthStrategy identifies the authentication strategy for this backend.
	// The actual authentication is handled by OutgoingAuthRegistry interface.
	// Examples: "pass_through", "token_exchange", "client_credentials", "oauth_proxy"
	AuthStrategy string

	// AuthMetadata contains strategy-specific authentication metadata.
	// This is opaque to the router and interpreted by the authenticator.
	AuthMetadata map[string]any

	// SessionAffinity indicates if requests from the same session
	// must be routed to this specific backend instance.
	SessionAffinity bool

	// HealthStatus indicates the current health of the backend.
	HealthStatus BackendHealthStatus

	// Metadata stores additional backend-specific information.
	Metadata map[string]string
}

// GetBackendCapabilityName returns the name to use when forwarding a request to the backend.
// If conflict resolution renamed the capability, this returns the original name that the backend expects.
// Otherwise, it returns the resolved name as-is.
//
// This method encapsulates the name translation logic for all capability types (tools, resources, prompts).
//
// ALWAYS use this method when forwarding capability calls to backends. Do NOT access
// OriginalCapabilityName directly, as it may be empty when no renaming occurred.
//
// Usage example:
//
//	target, _ := router.RouteTool(ctx, "fetch_fetch")  // Prefixed name from client
//	backendName := target.GetBackendCapabilityName("fetch_fetch")  // Returns "fetch"
//	client.CallTool(ctx, target, backendName, args)  // Backend receives original name
//
// This ensures correct behavior regardless of conflict resolution strategy:
//   - Prefix strategy: "fetch_fetch" → "fetch" (renamed, uses OriginalCapabilityName)
//   - Priority strategy: "list_issues" → "list_issues" (not renamed, returns resolvedName)
//   - Manual strategy: "custom_fetch" → "fetch" (renamed, uses OriginalCapabilityName)
func (t *BackendTarget) GetBackendCapabilityName(resolvedName string) string {
	if t.OriginalCapabilityName != "" {
		return t.OriginalCapabilityName
	}
	return resolvedName
}

// BackendHealthStatus represents the health state of a backend.
type BackendHealthStatus string

const (
	// BackendHealthy indicates the backend is healthy and accepting requests.
	BackendHealthy BackendHealthStatus = "healthy"

	// BackendDegraded indicates the backend is operational but experiencing issues.
	BackendDegraded BackendHealthStatus = "degraded"

	// BackendUnhealthy indicates the backend is not responding to health checks.
	BackendUnhealthy BackendHealthStatus = "unhealthy"

	// BackendUnknown indicates the backend health status is unknown.
	BackendUnknown BackendHealthStatus = "unknown"

	// BackendUnauthenticated indicates the backend is not authenticated.
	BackendUnauthenticated BackendHealthStatus = "unauthenticated"
)

// Backend represents a discovered backend MCP server workload.
type Backend struct {
	// ID is the unique identifier for this backend.
	ID string

	// Name is the human-readable name.
	Name string

	// BaseURL is the backend's MCP server URL.
	BaseURL string

	// TransportType is the MCP transport protocol.
	TransportType string

	// HealthStatus is the current health state.
	HealthStatus BackendHealthStatus

	// AuthStrategy identifies how to authenticate to this backend.
	AuthStrategy string

	// AuthMetadata contains strategy-specific auth configuration.
	AuthMetadata map[string]any

	// Metadata stores additional backend information.
	Metadata map[string]string
}

// Tool represents an MCP tool capability.
type Tool struct {
	// Name is the tool name (may conflict with other backends).
	Name string

	// Description describes what the tool does.
	Description string

	// InputSchema is the JSON Schema for tool parameters.
	InputSchema map[string]any

	// OutputSchema is the JSON Schema for tool output (optional).
	// Per MCP specification, this describes the structure of the tool's response.
	OutputSchema map[string]any

	// BackendID identifies the backend that provides this tool.
	BackendID string
}

// Resource represents an MCP resource capability.
type Resource struct {
	// URI is the resource URI (should be globally unique).
	URI string

	// Name is a human-readable name.
	Name string

	// Description describes the resource.
	Description string

	// MimeType is the resource's MIME type (optional).
	MimeType string

	// BackendID identifies the backend that provides this resource.
	BackendID string
}

// Prompt represents an MCP prompt capability.
type Prompt struct {
	// Name is the prompt name (may conflict with other backends).
	Name string

	// Description describes the prompt.
	Description string

	// Arguments are the prompt parameters.
	Arguments []PromptArgument

	// BackendID identifies the backend that provides this prompt.
	BackendID string
}

// PromptArgument represents a prompt parameter.
type PromptArgument struct {
	// Name is the argument name.
	Name string

	// Description describes the argument.
	Description string

	// Required indicates if the argument is mandatory.
	Required bool
}

// RoutingTable contains the mappings from capability names to backend targets.
// This is the output of the aggregation phase and input to the router.
// Placed in vmcp root package to avoid circular dependencies between
// aggregator and router packages.
//
// Note: Composite tools are NOT included here. They are executed by the composer
// package and do not route to a single backend.
type RoutingTable struct {
	// Tools maps tool names to their backend targets.
	// After conflict resolution, tool names are unique.
	Tools map[string]*BackendTarget

	// Resources maps resource URIs to their backend targets.
	Resources map[string]*BackendTarget

	// Prompts maps prompt names to their backend targets.
	Prompts map[string]*BackendTarget
}

// ConflictResolutionStrategy defines how to handle capability name conflicts.
// Placed in vmcp root package to be shared by config and aggregator packages.
type ConflictResolutionStrategy string

const (
	// ConflictStrategyPrefix prefixes all tools with workload identifier.
	ConflictStrategyPrefix ConflictResolutionStrategy = "prefix"

	// ConflictStrategyPriority uses explicit priority ordering (first wins).
	ConflictStrategyPriority ConflictResolutionStrategy = "priority"

	// ConflictStrategyManual requires explicit overrides for all conflicts.
	ConflictStrategyManual ConflictResolutionStrategy = "manual"
)

// HealthChecker performs health checks on backend MCP servers.
type HealthChecker interface {
	// CheckHealth checks if a backend is healthy and responding.
	// Returns the current health status and any error encountered.
	CheckHealth(ctx context.Context, target *BackendTarget) (BackendHealthStatus, error)
}

// BackendClient abstracts MCP protocol communication with backend servers.
// This interface handles the protocol-level details of calling backend MCP servers,
// supporting multiple transport types (HTTP, SSE, stdio, streamable-http).
//
//go:generate mockgen -destination=mocks/mock_backend_client.go -package=mocks -source=types.go BackendClient HealthChecker
type BackendClient interface {
	// CallTool invokes a tool on the backend MCP server.
	// Returns the tool output or an error.
	CallTool(ctx context.Context, target *BackendTarget, toolName string, arguments map[string]any) (map[string]any, error)

	// ReadResource retrieves a resource from the backend MCP server.
	// Returns the resource content or an error.
	ReadResource(ctx context.Context, target *BackendTarget, uri string) ([]byte, error)

	// GetPrompt retrieves a prompt from the backend MCP server.
	// Returns the rendered prompt text or an error.
	GetPrompt(ctx context.Context, target *BackendTarget, name string, arguments map[string]any) (string, error)

	// ListCapabilities queries a backend for its capabilities.
	// Returns tools, resources, and prompts exposed by the backend.
	ListCapabilities(ctx context.Context, target *BackendTarget) (*CapabilityList, error)
}

// CapabilityList contains the capabilities from a backend's MCP server.
// This is returned by BackendClient.ListCapabilities().
type CapabilityList struct {
	// Tools available on this backend.
	Tools []Tool

	// Resources available on this backend.
	Resources []Resource

	// Prompts available on this backend.
	Prompts []Prompt

	// SupportsLogging indicates if the backend supports MCP logging.
	SupportsLogging bool

	// SupportsSampling indicates if the backend supports MCP sampling.
	SupportsSampling bool
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

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

	// AuthConfig contains the typed authentication configuration for this backend.
	// The actual authentication is handled by OutgoingAuthRegistry interface.
	// If nil, the backend requires no authentication.
	AuthConfig *authtypes.BackendAuthStrategy

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
	// This occurs when:
	// - Health checks succeed but response times exceed the degraded threshold (slow but working)
	// - Backend just recovered from failures and is in a stabilizing state
	BackendDegraded BackendHealthStatus = "degraded"

	// BackendUnhealthy indicates the backend is not responding to health checks.
	BackendUnhealthy BackendHealthStatus = "unhealthy"

	// BackendUnknown indicates the backend health status is unknown.
	BackendUnknown BackendHealthStatus = "unknown"

	// BackendUnauthenticated indicates the backend is not authenticated.
	BackendUnauthenticated BackendHealthStatus = "unauthenticated"
)

// ToCRDStatus converts BackendHealthStatus to CRD-friendly status string.
// This maps internal health states to user-facing status values:
//   - healthy → ready
//   - degraded → degraded
//   - unhealthy → unavailable
//   - unauthenticated → unavailable (unauthenticated is a reason, not a status)
//   - unknown → unknown
func (s BackendHealthStatus) ToCRDStatus() string {
	switch s {
	case BackendHealthy:
		return "ready"
	case BackendDegraded:
		return "degraded"
	case BackendUnhealthy, BackendUnauthenticated:
		return "unavailable"
	case BackendUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// Condition represents a specific aspect of vMCP server status.
type Condition = metav1.Condition

// Phase represents the operational lifecycle phase of a vMCP server.
type Phase string

// Phase constants for vMCP server lifecycle.
const (
	PhasePending  Phase = "Pending"
	PhaseReady    Phase = "Ready"
	PhaseDegraded Phase = "Degraded"
	PhaseFailed   Phase = "Failed"
)

// Condition type constants for common vMCP conditions.
const (
	ConditionTypeBackendsDiscovered = "BackendsDiscovered"
	ConditionTypeReady              = "Ready"
	ConditionTypeAuthConfigured     = "AuthConfigured"
)

// Reason constants for condition reasons.
const (
	ReasonBackendDiscoverySucceeded = "BackendDiscoverySucceeded"
	ReasonBackendDiscoveryFailed    = "BackendDiscoveryFailed"
	ReasonServerReady               = "ServerReady"
	ReasonServerStarting            = "ServerStarting"
	ReasonServerDegraded            = "ServerDegraded"
	ReasonServerFailed              = "ServerFailed"
)

// DiscoveredBackend represents a backend server discovered by vMCP runtime.
// This type is shared with the Kubernetes operator CRD (VirtualMCPServer.Status.DiscoveredBackends).
type DiscoveredBackend struct {
	// Name is the name of the backend MCPServer
	Name string `json:"name"`

	// URL is the URL of the backend MCPServer
	// +optional
	URL string `json:"url,omitempty"`

	// Status is the current status of the backend (ready, degraded, unavailable, unknown).
	// Use BackendHealthStatus.ToCRDStatus() to populate this field.
	// +optional
	Status string `json:"status,omitempty"`

	// AuthConfigRef is the name of the discovered MCPExternalAuthConfig (if any)
	// +optional
	AuthConfigRef string `json:"authConfigRef,omitempty"`

	// AuthType is the type of authentication configured
	// +optional
	AuthType string `json:"authType,omitempty"`

	// LastHealthCheck is the timestamp of the last health check
	// +optional
	LastHealthCheck metav1.Time `json:"lastHealthCheck,omitempty"`

	// Message provides additional information about the backend status
	// +optional
	Message string `json:"message,omitempty"`
}

// DeepCopyInto copies the receiver into out. Required for Kubernetes CRD types.
func (in *DiscoveredBackend) DeepCopyInto(out *DiscoveredBackend) {
	*out = *in
	in.LastHealthCheck.DeepCopyInto(&out.LastHealthCheck)
}

// DeepCopy creates a deep copy of DiscoveredBackend. Required for Kubernetes CRD types.
func (in *DiscoveredBackend) DeepCopy() *DiscoveredBackend {
	if in == nil {
		return nil
	}
	out := new(DiscoveredBackend)
	in.DeepCopyInto(out)
	return out
}

// Status represents the runtime status of a vMCP server.
type Status struct {
	Phase              Phase               `json:"phase"`
	Message            string              `json:"message,omitempty"`
	Conditions         []Condition         `json:"conditions,omitempty"`
	DiscoveredBackends []DiscoveredBackend `json:"discoveredBackends,omitempty"`
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	Timestamp          time.Time           `json:"timestamp"`
}

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

	// AuthConfig contains the typed authentication configuration for this backend.
	// The actual authentication is handled by OutgoingAuthRegistry interface.
	// If nil, the backend requires no authentication.
	AuthConfig *authtypes.BackendAuthStrategy

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

// Content represents MCP content (text, image, audio, embedded resource).
// This is used by ToolCallResult to preserve the full content structure from backends.
type Content struct {
	// Type indicates the content type: "text", "image", "audio", "resource"
	Type string

	// Text is the content text (for TextContent)
	Text string

	// Data is the base64-encoded data (for ImageContent/AudioContent)
	Data string

	// MimeType is the MIME type (for ImageContent/AudioContent)
	MimeType string

	// URI is the resource URI (for EmbeddedResource)
	URI string
}

// ToolCallResult wraps a tool call response with metadata.
// This preserves both the tool output AND the _meta field from the backend MCP server.
type ToolCallResult struct {
	// Content is the tool output (text, image, etc.)
	// This is the array of content items returned by the backend.
	Content []Content

	// StructuredContent is structured output (preferred for composite tools and workflows).
	// If the backend MCP server provides StructuredContent, it is used directly.
	// Otherwise, this is populated by converting the Content array to a map:
	//   - First text item: key="text"
	//   - Additional text items: key="text_1", "text_2", etc.
	//   - Image items: key="image_0", "image_1", etc.
	// This allows templates to access fields via {{.steps.stepID.output.text}}.
	// Note: No JSON parsing is performed - backends must provide structured data explicitly.
	StructuredContent map[string]any

	// IsError indicates if the tool call failed.
	IsError bool

	// Meta contains protocol-level metadata from the backend (_meta field).
	// This includes progressToken, trace context, and custom backend metadata.
	// Per MCP specification, this field is optional and may be nil.
	Meta map[string]any
}

// ResourceReadResult wraps a resource read response with metadata.
// This preserves both the resource data AND the _meta field from the backend MCP server.
type ResourceReadResult struct {
	// Contents is the concatenated resource data.
	// When a resource has multiple contents (text or blob), they are concatenated
	// directly without separators. Text contents are converted to bytes, blob contents
	// are base64-decoded before concatenation.
	Contents []byte

	// MimeType is the content type of the resource.
	MimeType string

	// Meta contains protocol-level metadata from the backend (_meta field).
	// NOTE: Due to MCP SDK limitations, resources/read handlers cannot forward _meta
	// because they return []ResourceContents directly, not a result wrapper.
	// This field is preserved for future SDK improvements but may be nil.
	Meta map[string]any
}

// PromptGetResult wraps a prompt response with metadata.
// This preserves both the prompt messages AND the _meta field from the backend MCP server.
type PromptGetResult struct {
	// Messages is the concatenated prompt text from all messages.
	Messages string

	// Description is an optional description of the prompt.
	Description string

	// Meta contains protocol-level metadata from the backend (_meta field).
	// This includes progressToken, trace context, and custom backend metadata.
	// Per MCP specification, this field is optional and may be nil.
	Meta map[string]any
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
// All methods return wrapper types that preserve the _meta field from backend
// MCP server responses. Protocol-level metadata (progress tokens, trace context,
// custom metadata) is forwarded to clients where supported (tools and prompts).
// Note: Resource _meta forwarding is not currently supported due to MCP SDK handler
// signature limitations; the Meta field is preserved for future SDK improvements.
//
//go:generate mockgen -destination=mocks/mock_backend_client.go -package=mocks -source=types.go BackendClient HealthChecker
type BackendClient interface {
	// CallTool invokes a tool on the backend MCP server.
	// The meta parameter contains _meta fields from the client request that should be forwarded to the backend.
	// Returns the complete tool result including _meta field from the backend response.
	CallTool(
		ctx context.Context, target *BackendTarget, toolName string, arguments map[string]any, meta map[string]any,
	) (*ToolCallResult, error)

	// ReadResource retrieves a resource from the backend MCP server.
	// Returns the complete resource result including _meta field.
	ReadResource(ctx context.Context, target *BackendTarget, uri string) (*ResourceReadResult, error)

	// GetPrompt retrieves a prompt from the backend MCP server.
	// Returns the complete prompt result including _meta field.
	GetPrompt(ctx context.Context, target *BackendTarget, name string, arguments map[string]any) (*PromptGetResult, error)

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

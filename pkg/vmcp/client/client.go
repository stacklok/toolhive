// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides MCP protocol client implementation for communicating with backend servers.
//
// This package implements the BackendClient interface defined in the vmcp package,
// using the mark3labs/mcp-go SDK for protocol communication.
package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

const (
	// maxResponseSize is the maximum size in bytes for HTTP responses from backend MCP servers.
	// This protects against DoS attacks via memory exhaustion from malicious or compromised backends.
	//
	// The MCP specification does not define size limits, so we enforce a reasonable limit
	// to prevent unbounded memory allocation during JSON deserialization.
	//
	// Value: 100 MB
	// Rationale:
	//   - Allows large tool outputs, resources, and capability lists
	//   - Prevents memory exhaustion (a single large response could OOM the process)
	//   - Applied at HTTP transport layer before JSON deserialization
	//   - Backends needing larger responses should use pagination or streaming
	//
	// Note: This limit is enforced per HTTP response, not per MCP request.
	// A tools/list response with 1000 tools would be limited to 100MB total.
	maxResponseSize = 100 * 1024 * 1024 // 100 MB
)

// httpBackendClient implements vmcp.BackendClient using mark3labs/mcp-go HTTP client.
// It supports streamable-HTTP and SSE transports for backend MCP servers.
type httpBackendClient struct {
	// clientFactory creates MCP clients for backends.
	// Abstracted as a function to enable testing with mock clients.
	clientFactory func(ctx context.Context, target *vmcp.BackendTarget) (*client.Client, error)

	// registry manages authentication strategies for outgoing requests to backend MCP servers.
	// Must not be nil - use UnauthenticatedStrategy for no authentication.
	registry vmcpauth.OutgoingAuthRegistry
}

// NewHTTPBackendClient creates a new HTTP-based backend client.
// This client supports streamable-HTTP and SSE transports.
//
// The registry parameter manages authentication strategies for outgoing requests to backend MCP servers.
// It must not be nil. To disable authentication, use a registry configured with the
// "unauthenticated" strategy.
//
// Returns an error if registry is nil.
func NewHTTPBackendClient(registry vmcpauth.OutgoingAuthRegistry) (vmcp.BackendClient, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry cannot be nil; use UnauthenticatedStrategy for no authentication")
	}

	c := &httpBackendClient{
		registry: registry,
	}
	c.clientFactory = c.defaultClientFactory
	return c, nil
}

// roundTripperFunc is a function adapter for http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper interface.
func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// identityPropagatingRoundTripper propagates identity to backend HTTP requests.
// This ensures that identity information from the vMCP handler is available for authentication
// strategies that need it (e.g., token exchange).
type identityPropagatingRoundTripper struct {
	base     http.RoundTripper
	identity *auth.Identity
}

// RoundTrip implements http.RoundTripper by adding identity to the request context.
func (i *identityPropagatingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if i.identity != nil {
		// Add identity to the request's context
		ctx := auth.WithIdentity(req.Context(), i.identity)
		req = req.Clone(ctx)
	}
	return i.base.RoundTrip(req)
}

// authRoundTripper is an http.RoundTripper that adds authentication to backend requests.
// The authentication strategy is pre-resolved and validated at client creation time,
// eliminating per-request lookups and validation overhead.
type authRoundTripper struct {
	base         http.RoundTripper
	authStrategy vmcpauth.Strategy
	authConfig   *authtypes.BackendAuthStrategy
	target       *vmcp.BackendTarget
}

// RoundTrip implements http.RoundTripper by adding authentication headers to requests.
// The authentication strategy was pre-resolved and validated at client creation time,
// so this method simply applies the authentication without any lookups or validation.
func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone request to avoid modifying the original
	reqClone := req.Clone(req.Context())

	// Apply pre-resolved authentication strategy
	if err := a.authStrategy.Authenticate(reqClone.Context(), reqClone, a.authConfig); err != nil {
		return nil, fmt.Errorf("authentication failed for backend %s: %w", a.target.WorkloadID, err)
	}

	return a.base.RoundTrip(reqClone)
}

// resolveAuthStrategy resolves the authentication strategy for a backend target.
// It handles defaulting to "unauthenticated" when no auth config is specified.
// This method should be called once at client creation time to enable fail-fast
// behavior for invalid authentication configurations.
func (h *httpBackendClient) resolveAuthStrategy(target *vmcp.BackendTarget) (vmcpauth.Strategy, error) {
	// Default to unauthenticated if not specified
	strategyName := authtypes.StrategyTypeUnauthenticated
	if target.AuthConfig != nil {
		strategyName = target.AuthConfig.Type
	}

	// Resolve strategy from registry
	strategy, err := h.registry.GetStrategy(strategyName)
	if err != nil {
		return nil, fmt.Errorf("authentication strategy %q not found: %w", strategyName, err)
	}

	return strategy, nil
}

// defaultClientFactory creates mark3labs MCP clients for different transport types.
func (h *httpBackendClient) defaultClientFactory(ctx context.Context, target *vmcp.BackendTarget) (*client.Client, error) {
	// Build transport chain: size limit → context propagation → authentication → HTTP
	var baseTransport = http.DefaultTransport

	// Resolve authentication strategy ONCE at client creation time
	authStrategy, err := h.resolveAuthStrategy(target)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve authentication for backend %s: %w",
			target.WorkloadID, err)
	}

	// Validate auth config ONCE at client creation time
	if err := authStrategy.Validate(target.AuthConfig); err != nil {
		return nil, fmt.Errorf("invalid authentication configuration for backend %s: %w",
			target.WorkloadID, err)
	}

	logger.Debugf("Applied authentication strategy %q to backend %s", authStrategy.Name(), target.WorkloadID)

	// Add authentication layer with pre-resolved strategy
	baseTransport = &authRoundTripper{
		base:         baseTransport,
		authStrategy: authStrategy,
		authConfig:   target.AuthConfig,
		target:       target,
	}

	// Extract identity from context and propagate it to backend requests
	// This ensures authentication strategies (e.g., token exchange) can access identity
	identity, _ := auth.IdentityFromContext(ctx)
	baseTransport = &identityPropagatingRoundTripper{
		base:     baseTransport,
		identity: identity,
	}

	// Add size limit layer for DoS protection
	sizeLimitedTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := baseTransport.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		// Wrap response body with size limit
		resp.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.LimitReader(resp.Body, maxResponseSize),
			Closer: resp.Body,
		}
		return resp, nil
	})

	// Create HTTP client with configured transport chain
	// Set timeouts to prevent long-lived connections that require continuous listening
	httpClient := &http.Client{
		Transport: sizeLimitedTransport,
		Timeout:   30 * time.Second, // Prevent hanging on connections
	}

	var c *client.Client

	switch target.TransportType {
	case "streamable-http", "streamable":
		c, err = client.NewStreamableHttpClient(
			target.BaseURL,
			transport.WithHTTPTimeout(30*time.Second), // Set timeout instead of 0
			transport.WithHTTPBasicClient(httpClient),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create streamable-http client: %w", err)
		}

	case "sse":
		c, err = client.NewSSEMCPClient(
			target.BaseURL,
			transport.WithHTTPClient(httpClient),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create SSE client: %w", err)
		}

	default:
		return nil, fmt.Errorf("%w: %s (supported: streamable-http, sse)", vmcp.ErrUnsupportedTransport, target.TransportType)
	}

	// Start the client connection
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start client connection: %w", err)
	}

	// Note: Initialization is deferred to the caller (e.g., ListCapabilities)
	// so that ServerCapabilities can be captured and used for conditional querying
	return c, nil
}

// wrapBackendError wraps an error with the appropriate sentinel error based on error type.
// This enables type-safe error checking with errors.Is() instead of string matching.
//
// Error detection strategy (in order of preference):
// 1. Check for standard Go error types (context errors, net.Error, url.Error)
// 2. Fall back to string pattern matching for library-specific errors (MCP SDK, HTTP libs)
//
// Error chain preservation:
// The returned error wraps the sentinel error (ErrTimeout, ErrBackendUnavailable, etc.) with %w
// and formats the original error with %v. This means:
// - errors.Is() works for checking the sentinel error (e.g., errors.Is(err, vmcp.ErrTimeout))
// - errors.As() cannot access the underlying original error type
// This is a deliberate trade-off due to Go's limitation of one %w per fmt.Errorf call.
// If access to the underlying error type is needed in the future, consider implementing
// a custom error type with multiple Unwrap() methods (Go 1.20+).
func wrapBackendError(err error, backendID string, operation string) error {
	if err == nil {
		return nil
	}

	// 1. Type-based detection: Check for context deadline/cancellation
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: failed to %s for backend %s (timeout): %v",
			vmcp.ErrTimeout, operation, backendID, err)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: failed to %s for backend %s (cancelled): %v",
			vmcp.ErrCancelled, operation, backendID, err)
	}

	// 2. Type-based detection: Check for net.Error with Timeout() method
	// This handles network timeouts from the standard library
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: failed to %s for backend %s (timeout): %v",
			vmcp.ErrTimeout, operation, backendID, err)
	}

	// 3. String-based detection: Fall back to pattern matching for cases where
	// we don't have structured error types (MCP SDK, HTTP libraries with embedded status codes)
	// Authentication errors (401, 403, auth failures)
	if vmcp.IsAuthenticationError(err) {
		return fmt.Errorf("%w: failed to %s for backend %s: %v",
			vmcp.ErrAuthenticationFailed, operation, backendID, err)
	}

	// Timeout errors (deadline exceeded, timeout messages)
	if vmcp.IsTimeoutError(err) {
		return fmt.Errorf("%w: failed to %s for backend %s (timeout): %v",
			vmcp.ErrTimeout, operation, backendID, err)
	}

	// Connection errors (refused, reset, unreachable)
	if vmcp.IsConnectionError(err) {
		return fmt.Errorf("%w: failed to %s for backend %s (connection error): %v",
			vmcp.ErrBackendUnavailable, operation, backendID, err)
	}

	// Default to backend unavailable for unknown errors
	return fmt.Errorf("%w: failed to %s for backend %s: %v",
		vmcp.ErrBackendUnavailable, operation, backendID, err)
}

// initializeClient performs MCP protocol initialization handshake and returns server capabilities.
// This allows the caller to determine which optional features the server supports.
func initializeClient(ctx context.Context, c *client.Client) (*mcp.ServerCapabilities, error) {
	result, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "toolhive-vmcp",
				Version: "0.1.0",
			},
			Capabilities: mcp.ClientCapabilities{
				// Virtual MCP acts as a client to backends
				Roots: &struct {
					ListChanged bool `json:"listChanged,omitempty"`
				}{
					ListChanged: false,
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return &result.Capabilities, nil
}

// queryTools queries tools from a backend if the server advertises tool support.
func queryTools(ctx context.Context, c *client.Client, supported bool, backendID string) (*mcp.ListToolsResult, error) {
	if supported {
		result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			return nil, fmt.Errorf("failed to list tools from backend %s: %w", backendID, err)
		}
		return result, nil
	}
	logger.Debugf("Backend %s does not advertise tools capability, skipping tools query", backendID)
	return &mcp.ListToolsResult{Tools: []mcp.Tool{}}, nil
}

// queryResources queries resources from a backend if the server advertises resource support.
func queryResources(ctx context.Context, c *client.Client, supported bool, backendID string) (*mcp.ListResourcesResult, error) {
	if supported {
		result, err := c.ListResources(ctx, mcp.ListResourcesRequest{})
		if err != nil {
			return nil, fmt.Errorf("failed to list resources from backend %s: %w", backendID, err)
		}
		return result, nil
	}
	logger.Debugf("Backend %s does not advertise resources capability, skipping resources query", backendID)
	return &mcp.ListResourcesResult{Resources: []mcp.Resource{}}, nil
}

// queryPrompts queries prompts from a backend if the server advertises prompt support.
func queryPrompts(ctx context.Context, c *client.Client, supported bool, backendID string) (*mcp.ListPromptsResult, error) {
	if supported {
		result, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
		if err != nil {
			return nil, fmt.Errorf("failed to list prompts from backend %s: %w", backendID, err)
		}
		return result, nil
	}
	logger.Debugf("Backend %s does not advertise prompts capability, skipping prompts query", backendID)
	return &mcp.ListPromptsResult{Prompts: []mcp.Prompt{}}, nil
}

// convertContent converts mcp.Content to vmcp.Content.
// This preserves the full content structure from backend responses.
func convertContent(content mcp.Content) vmcp.Content {
	if textContent, ok := mcp.AsTextContent(content); ok {
		return vmcp.Content{
			Type: "text",
			Text: textContent.Text,
		}
	}
	if imageContent, ok := mcp.AsImageContent(content); ok {
		return vmcp.Content{
			Type:     "image",
			Data:     imageContent.Data,
			MimeType: imageContent.MIMEType,
		}
	}
	if audioContent, ok := mcp.AsAudioContent(content); ok {
		return vmcp.Content{
			Type:     "audio",
			Data:     audioContent.Data,
			MimeType: audioContent.MIMEType,
		}
	}
	// Handle embedded resources if needed
	// Unknown content types are marked as "unknown" type with no data
	logger.Warnf("Encountered unknown content type %T, marking as unknown content. "+
		"This may indicate missing support for embedded resources or other MCP content types.", content)
	return vmcp.Content{Type: "unknown"}
}

// ListCapabilities queries a backend for its MCP capabilities.
// Returns tools, resources, and prompts exposed by the backend.
// Only queries capabilities that the server advertises during initialization.
func (h *httpBackendClient) ListCapabilities(ctx context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	logger.Debugf("Querying capabilities from backend %s (%s)", target.WorkloadName, target.BaseURL)

	// Create a client for this backend (not yet initialized)
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			logger.Debugf("Failed to close client: %v", err)
		}
	}()

	// Initialize the client and get server capabilities
	serverCaps, err := initializeClient(ctx, c)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	logger.Debugf("Backend %s capabilities: tools=%v, resources=%v, prompts=%v",
		target.WorkloadID, serverCaps.Tools != nil, serverCaps.Resources != nil, serverCaps.Prompts != nil)

	// Query each capability type based on server advertisement
	// Check for nil BEFORE passing to functions to avoid interface{} nil pointer issues
	toolsResp, err := queryTools(ctx, c, serverCaps.Tools != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list tools")
	}

	resourcesResp, err := queryResources(ctx, c, serverCaps.Resources != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list resources")
	}

	promptsResp, err := queryPrompts(ctx, c, serverCaps.Prompts != nil, target.WorkloadID)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "list prompts")
	}

	// Convert MCP types to vmcp types
	capabilities := &vmcp.CapabilityList{
		Tools:     make([]vmcp.Tool, len(toolsResp.Tools)),
		Resources: make([]vmcp.Resource, len(resourcesResp.Resources)),
		Prompts:   make([]vmcp.Prompt, len(promptsResp.Prompts)),
	}

	// Convert tools
	for i, tool := range toolsResp.Tools {
		// Convert ToolInputSchema to map[string]any
		// The ToolInputSchema is a struct with Type, Properties, Required fields
		inputSchema := map[string]any{
			"type": tool.InputSchema.Type,
		}
		if tool.InputSchema.Properties != nil {
			inputSchema["properties"] = tool.InputSchema.Properties
		}
		if len(tool.InputSchema.Required) > 0 {
			inputSchema["required"] = tool.InputSchema.Required
		}
		if tool.InputSchema.Defs != nil {
			inputSchema["$defs"] = tool.InputSchema.Defs
		}

		capabilities.Tools[i] = vmcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: inputSchema,
			BackendID:   target.WorkloadID,
		}
	}

	// Convert resources
	for i, resource := range resourcesResp.Resources {
		capabilities.Resources[i] = vmcp.Resource{
			URI:         resource.URI,
			Name:        resource.Name,
			Description: resource.Description,
			MimeType:    resource.MIMEType,
			BackendID:   target.WorkloadID,
		}
	}

	// Convert prompts
	for i, prompt := range promptsResp.Prompts {
		args := make([]vmcp.PromptArgument, len(prompt.Arguments))
		for j, arg := range prompt.Arguments {
			args[j] = vmcp.PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			}
		}

		capabilities.Prompts[i] = vmcp.Prompt{
			Name:        prompt.Name,
			Description: prompt.Description,
			Arguments:   args,
			BackendID:   target.WorkloadID,
		}
	}

	// TODO: Query server capabilities to detect logging/sampling support
	// This requires additional MCP protocol support for capabilities introspection

	logger.Debugf("Backend %s capabilities: %d tools, %d resources, %d prompts",
		target.WorkloadName, len(capabilities.Tools), len(capabilities.Resources), len(capabilities.Prompts))

	return capabilities, nil
}

// CallTool invokes a tool on the backend MCP server.
// Returns the complete tool result including _meta field.
//
//nolint:gocyclo // this function is complex because it handles tool calls with various content types and error handling.
func (h *httpBackendClient) CallTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	logger.Debugf("Calling tool %s on backend %s", toolName, target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			logger.Debugf("Failed to close client: %v", err)
		}
	}()

	// Initialize the client
	if _, err := initializeClient(ctx, c); err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// Call the tool using the original capability name from the backend's perspective.
	// When conflict resolution renames tools (e.g., "fetch" → "fetch_fetch"),
	// we must use the original backend name when forwarding requests.
	backendToolName := target.GetBackendCapabilityName(toolName)
	if backendToolName != toolName {
		logger.Debugf("Translating tool name: %s (client-facing) → %s (backend)", toolName, backendToolName)
	}

	result, err := c.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      backendToolName,
			Arguments: arguments,
			Meta:      conversion.ToMCPMeta(meta),
		},
	})
	if err != nil {
		// Network/connection errors are operational errors
		return nil, fmt.Errorf("%w: tool call failed on backend %s: %w", vmcp.ErrBackendUnavailable, target.WorkloadID, err)
	}

	// Extract _meta field from backend response
	responseMeta := conversion.FromMCPMeta(result.Meta)

	// Log if tool returned IsError=true (MCP protocol-level error, not a transport error)
	// We still return the full result to preserve metadata and error details for the client
	if result.IsError {
		var errorMsg string
		if len(result.Content) > 0 {
			if textContent, ok := mcp.AsTextContent(result.Content[0]); ok {
				errorMsg = textContent.Text
			}
		}
		if errorMsg == "" {
			errorMsg = "tool execution error"
		}

		// Log with metadata for distributed tracing
		if responseMeta != nil {
			logger.Warnf("Tool %s on backend %s returned IsError=true: %s (meta: %+v)",
				toolName, target.WorkloadID, errorMsg, responseMeta)
		} else {
			logger.Warnf("Tool %s on backend %s returned IsError=true: %s", toolName, target.WorkloadID, errorMsg)
		}
		// Continue processing - we return the result with IsError flag and metadata preserved
	}

	// Convert MCP content to vmcp.Content array
	contentArray := make([]vmcp.Content, len(result.Content))
	for i, content := range result.Content {
		contentArray[i] = convertContent(content)
	}

	// Check for structured content first (preferred for composite tool step chaining).
	// StructuredContent allows templates to access nested fields directly via {{.steps.stepID.output.field}}.
	// Note: StructuredContent must be an object (map). Arrays or primitives are not supported.
	var structuredContent map[string]any
	if result.StructuredContent != nil {
		if structuredMap, ok := result.StructuredContent.(map[string]any); ok {
			logger.Debugf("Using structured content from tool %s on backend %s", toolName, target.WorkloadID)
			structuredContent = structuredMap
		} else {
			// StructuredContent is not an object - fall through to Content processing
			logger.Debugf("StructuredContent from tool %s on backend %s is not an object, falling back to Content",
				toolName, target.WorkloadID)
		}
	}

	// If no structured content, convert result contents to a map for backward compatibility.
	// MCP tools return an array of Content interface (TextContent, ImageContent, etc.).
	// Text content is stored under "text" key, accessible via {{.steps.stepID.output.text}}.
	if structuredContent == nil {
		structuredContent = conversion.ContentArrayToMap(contentArray)
	}

	return &vmcp.ToolCallResult{
		Content:           contentArray,
		StructuredContent: structuredContent,
		IsError:           result.IsError,
		Meta:              responseMeta,
	}, nil
}

// ReadResource retrieves a resource from the backend MCP server.
// Returns the complete resource result including _meta field.
func (h *httpBackendClient) ReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (*vmcp.ResourceReadResult, error) {
	logger.Debugf("Reading resource %s from backend %s", uri, target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			logger.Debugf("Failed to close client: %v", err)
		}
	}()

	// Initialize the client
	if _, err := initializeClient(ctx, c); err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// Read the resource using the original URI from the backend's perspective.
	// When conflict resolution renames resources, we must use the original backend URI.
	backendURI := target.GetBackendCapabilityName(uri)
	if backendURI != uri {
		logger.Debugf("Translating resource URI: %s (client-facing) → %s (backend)", uri, backendURI)
	}

	result, err := c.ReadResource(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{
			URI: backendURI,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("resource read failed on backend %s: %w", target.WorkloadID, err)
	}

	// Concatenate all resource contents
	// MCP resources can have multiple contents (text or blob)
	var data []byte
	var mimeType string
	for i, content := range result.Contents {
		// Try to convert to TextResourceContents
		if textContent, ok := mcp.AsTextResourceContents(content); ok {
			data = append(data, []byte(textContent.Text)...)
			if i == 0 && textContent.MIMEType != "" {
				mimeType = textContent.MIMEType
			}
		} else if blobContent, ok := mcp.AsBlobResourceContents(content); ok {
			// Blob is base64-encoded per MCP spec, decode it to bytes
			decoded, err := base64.StdEncoding.DecodeString(blobContent.Blob)
			if err != nil {
				logger.Warnf("Failed to decode base64 blob from resource %s on backend %s: %v",
					uri, target.WorkloadID, err)
				// Append raw blob as fallback
				data = append(data, []byte(blobContent.Blob)...)
			} else {
				data = append(data, decoded...)
			}
			if i == 0 && blobContent.MIMEType != "" {
				mimeType = blobContent.MIMEType
			}
		}
	}

	// Extract _meta field from backend response
	meta := conversion.FromMCPMeta(result.Meta)

	return &vmcp.ResourceReadResult{
		Contents: data,
		MimeType:  mimeType,
		Meta:      meta,
	}, nil
}

// GetPrompt retrieves a prompt from the backend MCP server.
// Returns the complete prompt result including _meta field.
func (h *httpBackendClient) GetPrompt(
	ctx context.Context,
	target *vmcp.BackendTarget,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	logger.Debugf("Getting prompt %s from backend %s", name, target.WorkloadName)

	// Create a client for this backend
	c, err := h.clientFactory(ctx, target)
	if err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "create client")
	}
	defer func() {
		if err := c.Close(); err != nil {
			logger.Debugf("Failed to close client: %v", err)
		}
	}()

	// Initialize the client
	if _, err := initializeClient(ctx, c); err != nil {
		return nil, wrapBackendError(err, target.WorkloadID, "initialize client")
	}

	// Get the prompt using the original prompt name from the backend's perspective.
	// When conflict resolution renames prompts, we must use the original backend name.
	backendPromptName := target.GetBackendCapabilityName(name)
	if backendPromptName != name {
		logger.Debugf("Translating prompt name: %s (client-facing) → %s (backend)", name, backendPromptName)
	}

	// Convert map[string]any to map[string]string
	stringArgs := make(map[string]string)
	for k, v := range arguments {
		stringArgs[k] = fmt.Sprintf("%v", v)
	}

	result, err := c.GetPrompt(ctx, mcp.GetPromptRequest{
		Params: mcp.GetPromptParams{
			Name:      backendPromptName,
			Arguments: stringArgs,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("prompt get failed on backend %s: %w", target.WorkloadID, err)
	}

	// Concatenate all prompt messages into a single string
	// MCP prompts return messages with role and content (Content interface)
	var prompt string
	for _, msg := range result.Messages {
		if msg.Role != "" {
			prompt += fmt.Sprintf("[%s] ", msg.Role)
		}
		// Try to convert content to TextContent
		if textContent, ok := mcp.AsTextContent(msg.Content); ok {
			prompt += textContent.Text + "\n"
		}
		// TODO: Handle other content types (image, audio, resource)
	}

	// Extract _meta field from backend response
	meta := conversion.FromMCPMeta(result.Meta)

	return &vmcp.PromptGetResult{
		Messages:    prompt,
		Description: result.Description,
		Meta:        meta,
	}, nil
}

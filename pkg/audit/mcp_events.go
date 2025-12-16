// Package audit provides MCP-specific audit event types and constants.
package audit

// MCP-specific event types based on the Model Context Protocol specification
const (
	// EventTypeMCPInitialize represents an MCP initialization event
	EventTypeMCPInitialize = "mcp_initialize"
	// EventTypeSSEConnection represents an SSE connection event
	EventTypeSSEConnection = "sse_connection"
	// EventTypeMCPToolCall represents an MCP tool call event
	EventTypeMCPToolCall = "mcp_tool_call"
	// EventTypeMCPToolsList represents an MCP tools list event
	EventTypeMCPToolsList = "mcp_tools_list"
	// EventTypeMCPResourceRead represents an MCP resource read event
	EventTypeMCPResourceRead = "mcp_resource_read"
	// EventTypeMCPResourcesList represents an MCP resources list event
	EventTypeMCPResourcesList = "mcp_resources_list"
	// EventTypeMCPPromptGet represents an MCP prompt get event
	EventTypeMCPPromptGet = "mcp_prompt_get"
	// EventTypeMCPPromptsList represents an MCP prompts list event
	EventTypeMCPPromptsList = "mcp_prompts_list"
	// EventTypeMCPNotification represents an MCP notification event
	EventTypeMCPNotification = "mcp_notification"
	// EventTypeMCPPing represents an MCP ping event
	EventTypeMCPPing = "mcp_ping"
	// EventTypeMCPLogging represents an MCP logging event
	EventTypeMCPLogging = "mcp_logging"
	// EventTypeMCPCompletion represents an MCP completion event
	EventTypeMCPCompletion = "mcp_completion"
	// EventTypeMCPRootsListChanged represents an MCP roots list changed notification
	EventTypeMCPRootsListChanged = "mcp_roots_list_changed"

	// Workflow-specific event types for vMCP composite workflow execution
	// EventTypeWorkflowStarted represents workflow execution start
	EventTypeWorkflowStarted = "vmcp_workflow_started"
	// EventTypeWorkflowCompleted represents successful workflow completion
	EventTypeWorkflowCompleted = "vmcp_workflow_completed"
	// EventTypeWorkflowFailed represents workflow failure
	EventTypeWorkflowFailed = "vmcp_workflow_failed"
	// EventTypeWorkflowTimedOut represents workflow timeout
	EventTypeWorkflowTimedOut = "vmcp_workflow_timed_out"
	// EventTypeWorkflowStepStarted represents workflow step execution start
	EventTypeWorkflowStepStarted = "vmcp_workflow_step_started"
	// EventTypeWorkflowStepCompleted represents successful step completion
	EventTypeWorkflowStepCompleted = "vmcp_workflow_step_completed"
	// EventTypeWorkflowStepFailed represents step failure
	EventTypeWorkflowStepFailed = "vmcp_workflow_step_failed"
	// EventTypeWorkflowStepSkipped represents conditional step skip
	EventTypeWorkflowStepSkipped = "vmcp_workflow_step_skipped"

	// Fallback event types for unrecognized or generic requests
	// EventTypeMCPRequest represents a generic MCP request when specific type cannot be determined
	EventTypeMCPRequest = "mcp_request"
	// EventTypeHTTPRequest represents a generic HTTP request (non-MCP)
	EventTypeHTTPRequest = "http_request"
)

// MCP target types for audit events
const (
	// TargetTypeTool represents a tool target
	TargetTypeTool = "tool"
	// TargetTypeResource represents a resource target
	TargetTypeResource = "resource"
	// TargetTypePrompt represents a prompt target
	TargetTypePrompt = "prompt"
	// TargetTypeServer represents a server target
	TargetTypeServer = "server"
	// TargetTypeWorkflow represents a workflow target
	TargetTypeWorkflow = "workflow"
	// TargetTypeWorkflowStep represents a workflow step target
	TargetTypeWorkflowStep = "workflow_step"
)

// MCP-specific target field keys
const (
	// TargetKeyType is the key for the target type in the target map
	TargetKeyType = "type"
	// TargetKeyName is the key for the target name in the target map
	TargetKeyName = "name"
	// TargetKeyURI is the key for the target URI in the target map
	TargetKeyURI = "uri"
	// TargetKeyMethod is the key for the MCP method in the target map
	TargetKeyMethod = "method"
	// TargetKeyEndpoint is the key for the endpoint in the target map
	TargetKeyEndpoint = "endpoint"
	// TargetKeyWorkflowID is the key for the unique workflow execution ID
	TargetKeyWorkflowID = "workflow_id"
	// TargetKeyWorkflowName is the key for the workflow definition name
	TargetKeyWorkflowName = "workflow_name"
	// TargetKeyStepID is the key for the step identifier
	TargetKeyStepID = "step_id"
	// TargetKeyStepType is the key for the step type (tool, elicitation)
	TargetKeyStepType = "step_type"
	// TargetKeyToolName is the key for the tool being called (for tool steps)
	TargetKeyToolName = "tool_name"
)

// MCP-specific subject field keys
const (
	// SubjectKeyUser is the key for the user in the subjects map
	SubjectKeyUser = "user"
	// SubjectKeyUserID is the key for the user ID in the subjects map
	SubjectKeyUserID = "user_id"
	// SubjectKeyClientName is the key for the client name in the subjects map
	SubjectKeyClientName = "client_name"
	// SubjectKeyClientVersion is the key for the client version in the subjects map
	SubjectKeyClientVersion = "client_version"
)

// MCP-specific source field keys for EventSource.Extra
const (
	// SourceExtraKeyUserAgent is the key for the user agent in the source extra map
	SourceExtraKeyUserAgent = "user_agent"
	// SourceExtraKeyRequestID is the key for the request ID in the source extra map
	SourceExtraKeyRequestID = "request_id"
	// SourceExtraKeySessionID is the key for the session ID in the source extra map
	SourceExtraKeySessionID = "session_id"
)

// MCP-specific metadata field keys for EventMetadata.Extra
const (
	// MetadataExtraKeyMCPVersion is the key for the MCP version in the metadata extra map
	MetadataExtraKeyMCPVersion = "mcp_version"
	// MetadataExtraKeyTransport is the key for the transport type in the metadata extra map
	MetadataExtraKeyTransport = "transport"
	// MetadataExtraKeyDuration is the key for the request duration in the metadata extra map
	MetadataExtraKeyDuration = "duration_ms"
	// MetadataExtraKeyResponseSize is the key for the response size in the metadata extra map
	MetadataExtraKeyResponseSize = "response_size_bytes"
	// MetadataExtraKeyRetryCount is the key for the number of retries performed
	MetadataExtraKeyRetryCount = "retry_count"
	// MetadataExtraKeyStepCount is the key for the total number of steps in a workflow
	MetadataExtraKeyStepCount = "step_count"
	// MetadataExtraKeyTimeout is the key for the workflow timeout in milliseconds
	MetadataExtraKeyTimeout = "timeout_ms"
)

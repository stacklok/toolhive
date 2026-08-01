// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package audit provides MCP-specific audit event types and constants.
//
// The constants are aliases of toolhive-core's audit package, kept so
// existing toolhive call sites don't need import churn. New code should
// import github.com/stacklok/toolhive-core/audit directly.
package audit

import coreaudit "github.com/stacklok/toolhive-core/audit"

// MCP-specific event types based on the Model Context Protocol specification
const (
	// EventTypeMCPInitialize represents an MCP initialization event
	EventTypeMCPInitialize = coreaudit.EventTypeMCPInitialize
	// EventTypeSSEConnection represents an SSE connection event
	EventTypeSSEConnection = coreaudit.EventTypeSSEConnection
	// EventTypeMCPToolCall represents an MCP tool call event
	EventTypeMCPToolCall = coreaudit.EventTypeMCPToolCall
	// EventTypeMCPToolsList represents an MCP tools list event
	EventTypeMCPToolsList = coreaudit.EventTypeMCPToolsList
	// EventTypeMCPResourceRead represents an MCP resource read event
	EventTypeMCPResourceRead = coreaudit.EventTypeMCPResourceRead
	// EventTypeMCPResourcesList represents an MCP resources list event
	EventTypeMCPResourcesList = coreaudit.EventTypeMCPResourcesList
	// EventTypeMCPPromptGet represents an MCP prompt get event
	EventTypeMCPPromptGet = coreaudit.EventTypeMCPPromptGet
	// EventTypeMCPPromptsList represents an MCP prompts list event
	EventTypeMCPPromptsList = coreaudit.EventTypeMCPPromptsList
	// EventTypeMCPNotification represents an MCP notification event
	EventTypeMCPNotification = coreaudit.EventTypeMCPNotification
	// EventTypeMCPPing represents an MCP ping event
	EventTypeMCPPing = coreaudit.EventTypeMCPPing
	// EventTypeMCPLogging represents an MCP logging event
	EventTypeMCPLogging = coreaudit.EventTypeMCPLogging
	// EventTypeMCPCompletion represents an MCP completion event
	EventTypeMCPCompletion = coreaudit.EventTypeMCPCompletion
	// EventTypeMCPRootsListChanged represents an MCP roots list changed notification
	EventTypeMCPRootsListChanged = coreaudit.EventTypeMCPRootsListChanged

	// Workflow-specific event types for vMCP composite workflow execution
	// EventTypeWorkflowStarted represents workflow execution start
	EventTypeWorkflowStarted = coreaudit.EventTypeWorkflowStarted
	// EventTypeWorkflowCompleted represents successful workflow completion
	EventTypeWorkflowCompleted = coreaudit.EventTypeWorkflowCompleted
	// EventTypeWorkflowFailed represents workflow failure
	EventTypeWorkflowFailed = coreaudit.EventTypeWorkflowFailed
	// EventTypeWorkflowTimedOut represents workflow timeout
	EventTypeWorkflowTimedOut = coreaudit.EventTypeWorkflowTimedOut
	// EventTypeWorkflowStepStarted represents workflow step execution start
	EventTypeWorkflowStepStarted = coreaudit.EventTypeWorkflowStepStarted
	// EventTypeWorkflowStepCompleted represents successful step completion
	EventTypeWorkflowStepCompleted = coreaudit.EventTypeWorkflowStepCompleted
	// EventTypeWorkflowStepFailed represents step failure
	EventTypeWorkflowStepFailed = coreaudit.EventTypeWorkflowStepFailed
	// EventTypeWorkflowStepSkipped represents conditional step skip
	EventTypeWorkflowStepSkipped = coreaudit.EventTypeWorkflowStepSkipped

	// Fallback event types for unrecognized or generic requests
	// EventTypeMCPRequest represents a generic MCP request when specific type cannot be determined
	EventTypeMCPRequest = coreaudit.EventTypeMCPRequest
	// EventTypeHTTPRequest represents a generic HTTP request (non-MCP)
	EventTypeHTTPRequest = coreaudit.EventTypeHTTPRequest
)

// MCP target types for audit events
const (
	// TargetTypeTool represents a tool target
	TargetTypeTool = coreaudit.TargetTypeTool
	// TargetTypeResource represents a resource target
	TargetTypeResource = coreaudit.TargetTypeResource
	// TargetTypePrompt represents a prompt target
	TargetTypePrompt = coreaudit.TargetTypePrompt
	// TargetTypeServer represents a server target
	TargetTypeServer = coreaudit.TargetTypeServer
	// TargetTypeWorkflow represents a workflow target
	TargetTypeWorkflow = coreaudit.TargetTypeWorkflow
	// TargetTypeWorkflowStep represents a workflow step target
	TargetTypeWorkflowStep = coreaudit.TargetTypeWorkflowStep
)

// MCP-specific target field keys
const (
	// TargetKeyType is the key for the target type in the target map
	TargetKeyType = coreaudit.TargetKeyType
	// TargetKeyName is the key for the target name in the target map
	TargetKeyName = coreaudit.TargetKeyName
	// TargetKeyURI is the key for the target URI in the target map
	TargetKeyURI = coreaudit.TargetKeyURI
	// TargetKeyMethod is the key for the MCP method in the target map
	TargetKeyMethod = coreaudit.TargetKeyMethod
	// TargetKeyEndpoint is the key for the endpoint in the target map
	TargetKeyEndpoint = coreaudit.TargetKeyEndpoint
	// TargetKeyWorkflowID is the key for the unique workflow execution ID
	TargetKeyWorkflowID = coreaudit.TargetKeyWorkflowID
	// TargetKeyWorkflowName is the key for the workflow definition name
	TargetKeyWorkflowName = coreaudit.TargetKeyWorkflowName
	// TargetKeyStepID is the key for the step identifier
	TargetKeyStepID = coreaudit.TargetKeyStepID
	// TargetKeyStepType is the key for the step type (tool, elicitation)
	TargetKeyStepType = coreaudit.TargetKeyStepType
	// TargetKeyToolName is the key for the tool being called (for tool steps)
	TargetKeyToolName = coreaudit.TargetKeyToolName
)

// MCP-specific subject field keys
const (
	// SubjectKeyUser is the key for the user in the subjects map
	SubjectKeyUser = coreaudit.SubjectKeyUser
	// SubjectKeyUserID is the key for the user ID in the subjects map
	SubjectKeyUserID = coreaudit.SubjectKeyUserID
	// SubjectKeyClientName is the key for the client name in the subjects map
	SubjectKeyClientName = coreaudit.SubjectKeyClientName
	// SubjectKeyClientVersion is the key for the client version in the subjects map
	SubjectKeyClientVersion = coreaudit.SubjectKeyClientVersion
)

// MCP-specific source field keys for EventSource.Extra
const (
	// SourceExtraKeyUserAgent is the key for the user agent in the source extra map
	SourceExtraKeyUserAgent = coreaudit.SourceExtraKeyUserAgent
	// SourceExtraKeyRequestID is the key for the request ID in the source extra map
	SourceExtraKeyRequestID = coreaudit.SourceExtraKeyRequestID
	// SourceExtraKeySessionID is the key for the session ID in the source extra map
	SourceExtraKeySessionID = coreaudit.SourceExtraKeySessionID
)

// MCP-specific metadata field keys for EventMetadata.Extra
const (
	// MetadataExtraKeyMCPVersion is the key for the MCP version in the metadata extra map
	MetadataExtraKeyMCPVersion = coreaudit.MetadataExtraKeyMCPVersion
	// MetadataExtraKeyTransport is the key for the transport type in the metadata extra map
	MetadataExtraKeyTransport = coreaudit.MetadataExtraKeyTransport
	// MetadataExtraKeyDuration is the key for the request duration in the metadata extra map
	MetadataExtraKeyDuration = coreaudit.MetadataExtraKeyDuration
	// MetadataExtraKeyResponseSize is the key for the response size in the metadata extra map
	MetadataExtraKeyResponseSize = coreaudit.MetadataExtraKeyResponseSize
	// MetadataExtraKeyRetryCount is the key for the number of retries performed
	MetadataExtraKeyRetryCount = coreaudit.MetadataExtraKeyRetryCount
	// MetadataExtraKeyStepCount is the key for the total number of steps in a workflow
	MetadataExtraKeyStepCount = coreaudit.MetadataExtraKeyStepCount
	// MetadataExtraKeyTimeout is the key for the workflow timeout in milliseconds
	MetadataExtraKeyTimeout = coreaudit.MetadataExtraKeyTimeout
)

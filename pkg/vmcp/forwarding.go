// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import "context"

//go:generate mockgen -destination=mocks/mock_forwarding.go -package=mocks -source=forwarding.go SamplingRequester ClientNotifier

// MCP wire method names for the server->client notifications the vMCP relay
// forwards. These are the on-the-wire JSON-RPC method strings and MUST stay in
// lockstep between the backend client's OnNotification handler (which matches on
// them) and the server's notifier adapter (which emits them). The mcpcompat mcp
// package does not export these, so they are defined here in the domain package
// as the single source of truth.
const (
	// MethodProgressNotification is the notifications/progress wire method.
	MethodProgressNotification = "notifications/progress"

	// MethodLogNotification is the notifications/message (logging) wire method.
	MethodLogNotification = "notifications/message"

	// MethodToolListChanged is the notifications/tools/list_changed wire method.
	MethodToolListChanged = "notifications/tools/list_changed"

	// MethodResourceListChanged is the notifications/resources/list_changed wire method.
	MethodResourceListChanged = "notifications/resources/list_changed"

	// MethodPromptListChanged is the notifications/prompts/list_changed wire method.
	MethodPromptListChanged = "notifications/prompts/list_changed"
)

// ListChangedKind identifies which capability list changed on a backend.
type ListChangedKind int

const (
	// ListChangedTools indicates a backend's tools/list_changed notification.
	ListChangedTools ListChangedKind = iota
	// ListChangedResources indicates a backend's resources/list_changed notification.
	ListChangedResources
	// ListChangedPrompts indicates a backend's prompts/list_changed notification.
	ListChangedPrompts
)

// ListChangedKindForMethod maps a wire notification method to its ListChangedKind.
// It returns false for any method that is not one of the three list_changed
// notifications, so callers (the backend client's OnNotification handler and the
// persistent session connector's notification handler) share one classification.
func ListChangedKindForMethod(method string) (ListChangedKind, bool) {
	switch method {
	case MethodToolListChanged:
		return ListChangedTools, true
	case MethodResourceListChanged:
		return ListChangedResources, true
	case MethodPromptListChanged:
		return ListChangedPrompts, true
	default:
		return 0, false
	}
}

// BackendListChangedNotifier receives notice that a backend's tool, resource, or
// prompt list changed (a notifications/{tools,resources,prompts}/list_changed
// notification from that backend). Implementations MUST be non-blocking: this is
// called directly from a backend client's OnNotification handler, which runs on
// the client's receive loop — a blocking implementation would stall that loop
// (and, on the mid-call path, the drain-ping barrier in
// drainServerToClientNotifications).
type BackendListChangedNotifier interface {
	// NotifyBackendListChanged records that backendID's capability list of the
	// given kind changed. It must return without blocking on I/O or downstream
	// locks.
	NotifyBackendListChanged(backendID string, kind ListChangedKind)
}

// SamplingRequester sends an MCP sampling (sampling/createMessage) request to the
// client and blocks for the response.
//
// This is the server->client forwarding seam for MCP sampling, mirroring
// ElicitationRequester: a backend that, mid tools/call, asks its caller to sample
// an LLM must have that request relayed to the downstream vMCP client. Callers
// (the backend client's sampling handler) depend only on this interface and the
// domain SamplingRequest/SamplingResult value types, never on the underlying SDK.
// The transport adapter (sdkSamplingAdapter in pkg/vmcp/server) is the sole point
// that translates to/from mcp-go types.
//
// The underlying SDK handles JSON-RPC ID correlation and session routing
// internally, so implementations do not track request IDs.
type SamplingRequester interface {
	// RequestSampling sends a sampling request and blocks until the client
	// responds or the context is cancelled. Implementations return an error when
	// the context carries no downstream session or the client did not advertise
	// the sampling capability.
	RequestSampling(ctx context.Context, req SamplingRequest) (*SamplingResult, error)
}

// ClientNotifier forwards a backend's mid-call server->client notifications
// (progress, logging) to the downstream vMCP client on the same session.
//
// This is the server->client forwarding seam for notifications. Callers (the
// backend client's OnNotification handler) depend only on this interface and the
// domain notification value types, never on the underlying SDK. The transport
// adapter (sdkNotifierAdapter in pkg/vmcp/server) wraps the SDK's
// SendNotificationToClient. Forwarding is best-effort: a missing downstream
// session is not an error worth surfacing to the backend, so implementations
// treat it as a no-op.
type ClientNotifier interface {
	// NotifyProgress forwards a notifications/progress message to the downstream
	// client. It is best-effort; a nil error does not guarantee delivery.
	NotifyProgress(ctx context.Context, n ProgressNotification) error

	// NotifyLog forwards a notifications/message (logging) message to the
	// downstream client. It is best-effort.
	NotifyLog(ctx context.Context, n LogMessage) error
}

// ClientForwarderBinder is implemented by a BackendClient that can have the
// server->client forwarding requesters bound after the SDK server is
// constructed.
//
// server.New evaluates the BackendClient before Serve builds the mcp-go server
// that the SDK adapters wrap (a construction-order inversion, the same one the
// late-bound elicitation requester solves). New type-asserts the concrete
// backend client to this interface and binds the real adapters once, before the
// server begins serving. A BackendClient that does not implement it simply does
// not forward server->client traffic.
type ClientForwarderBinder interface {
	// BindForwarders installs the elicitation, sampling, and notification
	// requesters used to relay a backend's mid-call server->client traffic to the
	// downstream client, plus the sink that receives a backend's list_changed
	// notifications (mid-call and, on the persistent session connector, idle). It
	// is called exactly once, before serving begins. Any of the arguments may be
	// nil to leave that forwarding path disabled: a nil listChanged means backend
	// list_changed notifications are dropped rather than propagated downstream.
	BindForwarders(
		elicitation ElicitationRequester, sampling SamplingRequester, notifier ClientNotifier, listChanged BackendListChangedNotifier,
	)
}

// SamplingRequest is the domain-typed sampling (sampling/createMessage) request.
//
// It mirrors, one-to-one, the mcp-go CreateMessageParams fields the vMCP relay
// forwards. The adapter in pkg/vmcp/server translates this to the SDK request
// type; the backend client's sampling handler builds it from the SDK request.
type SamplingRequest struct {
	// Messages is the conversation history to sample from.
	Messages []SamplingMessage

	// ModelPreferences expresses the backend's model-selection preferences. The
	// client may ignore them. Nil when the backend set none.
	ModelPreferences *ModelPreferences

	// SystemPrompt is an optional system prompt the backend wants to use.
	SystemPrompt string

	// IncludeContext requests context be attached ("none", "thisServer",
	// "allServers"). The client may ignore it.
	IncludeContext string

	// Temperature is the sampling temperature.
	Temperature float64

	// MaxTokens is the maximum number of tokens to sample.
	MaxTokens int

	// StopSequences are sequences that, when generated, stop sampling.
	StopSequences []string

	// Metadata is optional provider-specific metadata passed through to the LLM.
	Metadata any
}

// SamplingMessage is a single message in a sampling conversation. Content stays
// typed as any (a text/image/audio content block) matching the SDK's wire shape,
// which the vMCP relay passes through without inspection.
type SamplingMessage struct {
	// Role is the message role ("user" or "assistant").
	Role string

	// Content is the message content block (text/image/audio).
	Content any
}

// ModelPreferences mirrors the mcp-go model-selection preferences. All fields
// are advisory.
type ModelPreferences struct {
	// Hints are ordered model-selection hints.
	Hints []ModelHint

	// CostPriority weights cost in model selection (0..1).
	CostPriority float64

	// SpeedPriority weights latency in model selection (0..1).
	SpeedPriority float64

	// IntelligencePriority weights capability in model selection (0..1).
	IntelligencePriority float64
}

// ModelHint is a single model-selection hint.
type ModelHint struct {
	// Name is treated by the client as a substring of a model name.
	Name string
}

// SamplingResult is the domain-typed sampling response. Content stays typed as
// any, matching the SDK's wire shape.
type SamplingResult struct {
	// Role is the role of the sampled message ("assistant").
	Role string

	// Content is the sampled content block (text/image/audio).
	Content any

	// Model is the name of the model that generated the message.
	Model string

	// StopReason is the reason sampling stopped, if known.
	StopReason string
}

// ProgressNotification is the domain-typed notifications/progress message.
type ProgressNotification struct {
	// ProgressToken correlates the notification with the request that requested
	// progress. It is an opaque token (string or number) passed through unchanged.
	ProgressToken any

	// Progress is the current progress value.
	Progress float64

	// Total is the optional total against which Progress is measured. Zero means
	// unset.
	Total float64

	// Message is an optional human-readable progress description.
	Message string
}

// LogMessage is the domain-typed notifications/message (logging) message.
type LogMessage struct {
	// Level is the syslog-style severity ("debug", "info", ..., "emergency").
	Level string

	// Logger is an optional logger name.
	Logger string

	// Data is the log payload (any JSON value).
	Data any
}

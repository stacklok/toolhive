// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/pagination"
)

const (
	// maxBackendResponseSize caps each HTTP response body for streamable-HTTP
	// backends to prevent memory exhaustion. Not applied to SSE transports —
	// see createMCPClient for the rationale.
	maxBackendResponseSize = 100 * 1024 * 1024 // 100 MB

	// defaultBackendRequestTimeout is the wall-clock deadline for individual
	// streamable-HTTP requests. Applied at both the http.Client and SDK layers
	// (defense-in-depth). Not used for SSE, whose stream lifetime is unbounded.
	defaultBackendRequestTimeout = 30 * time.Second
)

// ChangeKind identifies which capability class a backend reported changed via
// ListChangedSink. Using a typed constant (rather than a bare string) means a
// typo is a compile error at the producer and consumer instead of a silent
// no-op.
type ChangeKind string

const (
	// KindTools is reported when a backend emits notifications/tools/list_changed.
	KindTools ChangeKind = "tools"

	// KindResources is reported when a backend emits
	// notifications/resources/list_changed. Per MCP 2025-11-25 there is no
	// separate wire method for resource TEMPLATE changes, so this kind also
	// covers a resync of the backend's resource templates (see
	// resyncSessionResources in pkg/vmcp/server).
	KindResources ChangeKind = "resources"

	// KindPrompts is reported when a backend emits
	// notifications/prompts/list_changed.
	KindPrompts ChangeKind = "prompts"
)

// ListChangedSink is invoked when a persistent backend connection observes a
// notification this package consumes asynchronously (outside any in-flight
// call): notifications/tools/list_changed (kind=KindTools),
// notifications/resources/list_changed (kind=KindResources, which also covers
// resource templates — MCP 2025-11-25 has no separate wire method for those),
// and notifications/prompts/list_changed (kind=KindPrompts).
//
// The sink is invoked on the mcpcompat client's receive-loop goroutine (see
// Client.dispatch in toolhive-core), so implementations MUST be non-blocking:
// they must hand the work off (e.g. set a dirty flag / signal a worker) and
// return immediately. A sink that does real work (network I/O, cache purges)
// inline stalls that backend's entire notification delivery and lets a
// misbehaving backend amplify one notification into unbounded work. The ctx
// passed here is only for the hand-off; long-lived resync work must run under
// a caller-owned, cancellable context, not this one.
type ListChangedSink func(ctx context.Context, backendWorkloadID string, kind ChangeKind)

// newListChangedNotificationHandler builds the OnNotification handler
// registered by createMCPClient when sink is non-nil. It dispatches
// notifications/tools/list_changed (kind=KindTools),
// notifications/resources/list_changed (kind=KindResources, also covering
// resource templates), and notifications/prompts/list_changed
// (kind=KindPrompts) to sink, and log-only handles notifications/message;
// every other notification method is ignored — this handler is not the
// mid-call server->client relay (that is pkg/vmcp/client's per-call
// notification forwarder), it only feeds the session-registration resync path.
func newListChangedNotificationHandler(workloadID string, sink ListChangedSink) func(mcp.JSONRPCNotification) {
	return func(n mcp.JSONRPCNotification) {
		switch n.Method {
		case vmcp.MethodToolsListChangedNotification:
			sink(context.Background(), workloadID, KindTools)
		case vmcp.MethodResourcesListChangedNotification:
			sink(context.Background(), workloadID, KindResources)
		case vmcp.MethodPromptsListChangedNotification:
			sink(context.Background(), workloadID, KindPrompts)
		case vmcp.MethodLogNotification:
			// Out-of-call backend log messages are not relayed to the downstream
			// client on this path; log so the signal is visible rather than
			// silently dropped.
			slog.Debug("backend log notification received outside call", "backendID", workloadID)
		default:
			// Other notification types (resource subscription updates, ...) are
			// out of scope here.
		}
	}
}

// httpRoundTripperFunc adapts a plain function to http.RoundTripper.
type httpRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f httpRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// authRoundTripper adds pre-resolved authentication to outgoing backend requests.
type authRoundTripper struct {
	base         http.RoundTripper
	authStrategy vmcpauth.Strategy
	authConfig   *authtypes.BackendAuthStrategy
	target       *vmcp.BackendTarget
}

func (a *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	reqClone := req.Clone(req.Context())
	if err := a.authStrategy.Authenticate(reqClone.Context(), reqClone, a.authConfig); err != nil {
		return nil, fmt.Errorf("authentication failed for backend %s: %w", a.target.WorkloadID, err)
	}
	return a.base.RoundTrip(reqClone)
}

// identityRoundTripper propagates a fallback identity to outgoing backend
// requests when the request context carries none. It is the session-backed
// twin of identityPropagatingRoundTripper in pkg/vmcp/client/client.go, which
// holds the canonical description of the #5323 fallback-only identity
// invariant.
//
// Differences from the canonical twin:
//   - No isHealthCheck propagation. Health probes do not flow through
//     session-backed clients — they go through the per-call client in
//     pkg/vmcp/client. If a future change routes health probes through this
//     connector, mirror the isHealthCheck pattern from client.go so the
//     Close() DELETE built from context.Background() retains the marker.
//
// Consolidation is tracked by #5333; until then, keep the fallback-only
// invariant in sync across both implementations.
type identityRoundTripper struct {
	base http.RoundTripper
	// fallbackIdentity is injected only when req.Context() carries no identity.
	// It must never override a non-nil identity present on the request context.
	//
	// Shared across all concurrent RoundTrip invocations on this transport; the
	// pointed-to *auth.Identity (including UpstreamTokens) MUST be treated as
	// immutable — see pkg/auth/identity.go.
	fallbackIdentity *auth.Identity
}

// RoundTrip preserves any identity already on req.Context() and only injects
// the captured fallback when the request context carries no identity.
func (i *identityRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, hasIdentity := auth.IdentityFromContext(req.Context()); !hasIdentity && i.fallbackIdentity != nil {
		ctx := auth.WithIdentity(req.Context(), i.fallbackIdentity)
		req = req.Clone(ctx)
	}
	return i.base.RoundTrip(req)
}

// Compile-time assertion: mcpSession must implement Session.
var _ Session = (*mcpSession)(nil)

// mcpSession wraps a persistent mcpcompat MCP client for one backend.
// It is created once per backend during MakeSession and closed when the session ends.
//
// Phase 1 limitation — no reconnection: if the underlying transport drops
// (network error, server restart, SSE stream EOF), all subsequent operations
// on this backend will fail with the transport error. The session must be
// closed and a new one created to reconnect. This affects SSE backends more
// visibly because SSE uses a single long-lived HTTP stream; streamable-HTTP
// backends open a new connection per request and are therefore more resilient.
type mcpSession struct {
	client           *mcpclient.Client
	target           *vmcp.BackendTarget // bound at creation; used for capability name translation
	backendSessionID string              // backend-assigned session ID (may be empty)
}

// SessionID returns the backend-assigned session ID.
func (c *mcpSession) SessionID() string { return c.backendSessionID }

// Close closes the underlying MCP client transport.
func (c *mcpSession) Close() error { return c.client.Close() }

// CallTool invokes a named tool on this backend.
func (c *mcpSession) CallTool(
	ctx context.Context,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	backendName := c.target.GetBackendCapabilityName(toolName)
	if backendName != toolName {
		slog.Debug("Translating tool name", "clientName", toolName, "backendName", backendName)
	}

	result, err := c.client.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      backendName,
			Arguments: arguments,
			Meta:      conversion.ToMCPMeta(telemetry.MetaWithTraceContext(ctx, meta)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("tool %q call failed on backend %s: %w", toolName, c.target.WorkloadID, err)
	}

	contentArray := conversion.ConvertMCPContents(result.Content)

	var structuredContent map[string]any
	if result.StructuredContent != nil {
		if m, ok := result.StructuredContent.(map[string]any); ok {
			structuredContent = m
		}
	}
	if structuredContent == nil {
		structuredContent = conversion.ContentArrayToMap(contentArray)
	}

	return &vmcp.ToolCallResult{
		Content:           contentArray,
		StructuredContent: structuredContent,
		IsError:           result.IsError,
		Meta:              conversion.FromMCPMeta(result.Meta),
	}, nil
}

// ReadResource reads a resource from this backend.
func (c *mcpSession) ReadResource(
	ctx context.Context,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	backendURI := c.target.GetBackendCapabilityName(uri)
	if backendURI != uri {
		slog.Debug("Translating resource URI", "clientURI", uri, "backendURI", backendURI)
	}

	// Forward-compat: mcpcompat drops Params.Meta on this path today (no-op on
	// the wire). See pkg/vmcp/client's TestOutboundMetaTraceContext for
	// details/tripwire.
	result, err := c.client.ReadResource(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{
			URI:  backendURI,
			Meta: conversion.ToMCPMeta(telemetry.MetaWithTraceContext(ctx, nil)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("resource %q read failed on backend %s: %w", uri, c.target.WorkloadID, err)
	}

	return &vmcp.ResourceReadResult{
		Contents: conversion.ConvertMCPResourceContents(result.Contents),
		Meta:     conversion.FromMCPMeta(result.Meta),
	}, nil
}

// GetPrompt retrieves a prompt from this backend.
func (c *mcpSession) GetPrompt(
	ctx context.Context,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	backendName := c.target.GetBackendCapabilityName(name)
	if backendName != name {
		slog.Debug("Translating prompt name", "clientName", name, "backendName", backendName)
	}

	stringArgs := conversion.ConvertPromptArguments(arguments)

	// Forward-compat: mcpcompat drops Params.Meta on this path today (no-op on
	// the wire). See pkg/vmcp/client's TestOutboundMetaTraceContext for
	// details/tripwire.
	result, err := c.client.GetPrompt(ctx, mcp.GetPromptRequest{
		Params: mcp.GetPromptParams{
			Name:      backendName,
			Arguments: stringArgs,
			Meta:      conversion.ToMCPMeta(telemetry.MetaWithTraceContext(ctx, nil)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("prompt %q get failed on backend %s: %w", name, c.target.WorkloadID, err)
	}

	return &vmcp.PromptGetResult{
		Messages:    conversion.ConvertMCPPromptMessages(result.Messages),
		Description: result.Description,
		Meta:        conversion.FromMCPMeta(result.Meta),
	}, nil
}

// NewHTTPConnector returns a function that creates an HTTP-based (streamable-HTTP
// or SSE) persistent backend Session for each backend.
//
// registry provides the authentication strategy for outgoing backend requests.
// Pass a registry configured with the "unauthenticated" strategy to disable auth.
//
// A single secrets.EnvironmentProvider is constructed once per connector and
// shared across every session it creates; its lifetime matches the connector's.
// It is consumed by BuildHeaderForwardTripper to resolve secret-backed entries
// in target.HeaderForward.
//
// The returned function's sink parameter, when non-nil, enables persistent
// backend-notification consumption for this backend connection — see
// createMCPClient for what that does and does not enable (nil-sink callers are
// completely unaffected: no OnNotification handler is registered and no
// standalone GET stream is opened).
func NewHTTPConnector(registry vmcpauth.OutgoingAuthRegistry) func(
	ctx context.Context,
	target *vmcp.BackendTarget,
	identity *auth.Identity,
	sessionHint string,
	sink ListChangedSink,
) (Session, *vmcp.CapabilityList, error) {
	provider := secrets.NewEnvironmentProvider()
	return func(
		ctx context.Context,
		target *vmcp.BackendTarget,
		identity *auth.Identity,
		sessionHint string,
		sink ListChangedSink,
	) (Session, *vmcp.CapabilityList, error) {
		c, err := createMCPClient(ctx, target, identity, registry, sessionHint, provider, sink)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create MCP client for backend %s: %w", target.WorkloadID, err)
		}

		caps, err := initAndQueryCapabilities(ctx, c, target)
		if err != nil {
			_ = c.Close()
			return nil, nil, fmt.Errorf("failed to initialise backend %s: %w", target.WorkloadID, err)
		}

		// Extract the backend-assigned session ID when the transport supports it.
		// Streamable-HTTP servers send an Mcp-Session-Id response header during
		// Initialize; the mcpcompat transport captures it internally and exposes
		// it via GetSessionId(). SSE transports do not assign a session ID, so
		// the field remains empty for those backends.
		var backendSessionID string
		if sh, ok := c.GetTransport().(*mcptransport.StreamableHTTP); ok {
			backendSessionID = sh.GetSessionId()
		}

		return &mcpSession{client: c, target: target, backendSessionID: backendSessionID}, caps, nil
	}
}

// createMCPClient builds and starts a mcpcompat MCP client for target.
// The transport is started with context.Background() so its lifetime is bound
// to client.Close(), not to any caller-supplied init context.
// sessionHint, when non-empty, is passed as the initial Mcp-Session-Id for
// streamable-HTTP transports so the backend can resume an existing session.
//
// ctx is used only to resolve secret-backed entries in target.HeaderForward at
// client-creation time; the transport itself is started with context.Background()
// as described above. provider supplies values for those secret-backed headers.
//
// sink is gated STRICTLY on non-nil: when nil, this function's behavior is
// byte-for-byte identical to before sink existed (no OnNotification handler is
// registered, and the streamable-HTTP branch does not enable continuous
// listening). When non-nil, an OnNotification handler is registered (before
// c.Start, per the mcpcompat client's registration contract) that invokes sink
// on notifications/tools/list_changed, notifications/resources/list_changed
// (which also covers resource templates), and notifications/prompts/list_changed,
// and log-only handles notifications/message. For the streamable-HTTP transport,
// a non-nil sink also enables mcptransport.WithContinuousListening() — required
// for the backend's asynchronous (outside any in-flight call) notifications to
// reach this client at all — since some backends hang when a standalone GET
// stream is opened against them, this must stay opt-in (#5748 R3).
func createMCPClient(
	ctx context.Context,
	target *vmcp.BackendTarget,
	identity *auth.Identity,
	registry vmcpauth.OutgoingAuthRegistry,
	sessionHint string,
	provider secrets.Provider,
	sink ListChangedSink,
) (*mcpclient.Client, error) {
	// Resolve and validate the auth strategy once at client creation time.
	strategyName := authtypes.StrategyTypeUnauthenticated
	if target.AuthConfig != nil {
		strategyName = target.AuthConfig.Type
	}
	strategy, err := registry.GetStrategy(strategyName)
	if err != nil {
		return nil, fmt.Errorf("auth strategy %q not found: %w", strategyName, err)
	}
	if err := strategy.Validate(target.AuthConfig); err != nil {
		return nil, fmt.Errorf("invalid auth config for backend %s: %w", target.WorkloadID, err)
	}

	slog.Debug("Applied authentication strategy", "strategy", strategy.Name(), "backendID", target.WorkloadID)

	// Build shared transport chain (innermost first → outermost):
	//   http.DefaultTransport → authRoundTripper → identityRoundTripper → headerForwardRoundTripper
	// On an outbound request, the outermost stage runs first: header-forward
	// injects its headers onto a request that does not yet carry auth/identity
	// headers, then inner stages run and call Set() unconditionally so any
	// overlapping name they care about (Authorization, identity headers) wins on
	// the wire. Restricted header names (Host, hop-by-hop, X-Forwarded-*) are
	// rejected at resolve time by resolveHeaderForward, so user-supplied
	// HeaderForward cannot inject them in the first place.
	// The per-transport sections below may add a size-limiting wrapper on top.
	base := http.RoundTripper(http.DefaultTransport)
	base = &authRoundTripper{
		base:         base,
		authStrategy: strategy,
		authConfig:   target.AuthConfig,
		target:       target,
	}
	// The identity captured here is a fallback used only when the per-request
	// context carries no identity (e.g. mcp-go's Close() DELETE built from
	// context.Background()). Normal backend requests preserve the freshly
	// refreshed identity placed on the request context by
	// auth.TokenValidator.Middleware (see issue #5323).
	base = &identityRoundTripper{base: base, fallbackIdentity: identity}
	// Forwarded headers ride the request context (set by headerforward.CaptureMiddleware
	// at the vMCP server's incoming edge) and are merged into the per-session backend
	// header-forward config here. The session is created once per request, so the
	// captured headers are stable for the session's lifetime.
	mergedHeaderForward, err := mergeForwardedHeaders(target.HeaderForward, headerforward.ForwardedHeadersFromContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("backend %s: %w", target.WorkloadID, err)
	}
	base, err = headerforward.BuildHeaderForwardTripper(ctx, base, mergedHeaderForward, provider, target.WorkloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to build header-forward transport for backend %s: %w", target.WorkloadID, err)
	}

	var c *mcpclient.Client
	switch target.TransportType {
	case "streamable-http", "streamable":
		// "streamable" is a legacy alias for "streamable-http".
		//
		// For streamable-HTTP, each MCP call is a single bounded HTTP
		// request/response pair, so a per-response body size limit is safe and
		// correct. http.Client.Timeout provides a hard wall-clock deadline;
		// WithHTTPTimeout additionally wraps each SDK request in a
		// context.WithTimeout so the mcpcompat transport surfaces a descriptive
		// error before the stdlib deadline fires. Both are set to
		// defaultBackendRequestTimeout: defense-in-depth.
		sizeLimited := httpRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := base.RoundTrip(req)
			if err != nil {
				return nil, err
			}
			resp.Body = struct {
				io.Reader
				io.Closer
			}{
				Reader: io.LimitReader(resp.Body, maxBackendResponseSize),
				Closer: resp.Body,
			}
			return resp, nil
		})
		httpClient := &http.Client{
			Transport: sizeLimited,
			Timeout:   defaultBackendRequestTimeout,
		}
		streamableOpts := []mcptransport.StreamableHTTPCOption{
			mcptransport.WithHTTPTimeout(defaultBackendRequestTimeout),
			mcptransport.WithHTTPBasicClient(httpClient),
		}
		if sessionHint != "" {
			streamableOpts = append(streamableOpts, mcptransport.WithSession(sessionHint))
		}
		if sink != nil {
			// A standalone GET stream is the only way this client can receive a
			// notification the backend emits outside an in-flight call (e.g.
			// tools/list_changed after a registration-time capability change).
			// Gated strictly on sink != nil: some backends hang when this stream
			// is opened against them (#5748 R3), so it must stay opt-in.
			streamableOpts = append(streamableOpts, mcptransport.WithContinuousListening())
		}
		c, err = mcpclient.NewStreamableHttpClient(target.BaseURL, streamableOpts...)
	case "sse":
		// For SSE, the entire session is delivered as one long-lived HTTP
		// response body. Applying io.LimitReader to that body would silently
		// terminate the connection after maxBackendResponseSize cumulative bytes
		// — not per-event — which is wrong. Individual event size is bounded by
		// the backend; operation deadlines are enforced via context cancellation.
		//
		// http.Client.Timeout is also omitted: it caps the full round-trip
		// including body reads, which would kill the stream after the timeout.
		httpClient := &http.Client{Transport: base}
		c, err = mcpclient.NewSSEMCPClient(
			target.BaseURL,
			mcptransport.WithHTTPClient(httpClient),
		)
	default:
		return nil, fmt.Errorf("%w: %s (supported: streamable-http, sse)",
			vmcp.ErrUnsupportedTransport, target.TransportType)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create %s client: %w", target.TransportType, err)
	}

	// Register the notification handler before Start (mirrors the per-call
	// client in pkg/vmcp/client, and matches the mcpcompat client's own
	// registration contract): the handler dispatches
	// notifications/tools/list_changed, notifications/resources/list_changed,
	// and notifications/prompts/list_changed to sink and logs
	// notifications/message. Strictly gated on sink != nil so a nil-sink caller
	// registers no handler at all — this function's behavior for that caller is
	// unchanged.
	if sink != nil {
		c.OnNotification(newListChangedNotificationHandler(target.WorkloadID, sink))
	}

	// Start the transport with context.Background() so that the transport's
	// lifetime is scoped to the session (terminated by client.Close()) rather
	// than to the per-backend init timeout context. The init timeout context
	// is used only for the Initialize handshake and capability queries in
	// initAndQueryCapabilities, both of which have bounded duration.
	// Without this, the SSE transport would tear down its persistent read
	// goroutine when the init goroutine's defer-cancel fires after init completes.
	if err := c.Start(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to start client: %w", err)
	}

	return c, nil
}

// initAndQueryCapabilities runs the MCP Initialize handshake then discovers
// all capabilities (tools, resources, prompts) from the backend.
func initAndQueryCapabilities(
	ctx context.Context,
	c *mcpclient.Client,
	target *vmcp.BackendTarget,
) (*vmcp.CapabilityList, error) {
	result, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "toolhive-vmcp",
				Version: versions.Version,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize failed: %w", err)
	}

	serverCaps := result.Capabilities
	caps := &vmcp.CapabilityList{}

	if serverCaps.Tools != nil {
		tools, err := queryBackendTools(ctx, c, target)
		if err != nil {
			return nil, err
		}
		caps.Tools = tools
	}

	if serverCaps.Resources != nil {
		resources, err := queryBackendResources(ctx, c, target)
		if err != nil {
			return nil, err
		}
		caps.Resources = resources
	}

	if serverCaps.Prompts != nil {
		prompts, err := queryBackendPrompts(ctx, c, target)
		if err != nil {
			return nil, err
		}
		caps.Prompts = prompts
	}

	slog.Debug("Backend capabilities",
		"backendID", target.WorkloadID,
		"tools", len(caps.Tools),
		"resources", len(caps.Resources),
		"prompts", len(caps.Prompts),
	)

	return caps, nil
}

// queryBackendTools lists the backend's tools, following MCP pagination cursors so a
// backend that paginates (mcpcompat paginates at DefaultPageSize=1000) contributes its
// complete tool set rather than only the first page.
func queryBackendTools(ctx context.Context, c *mcpclient.Client, target *vmcp.BackendTarget) ([]vmcp.Tool, error) {
	tools, err := pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Tool, mcp.Cursor, error) {
		req := mcp.ListToolsRequest{}
		req.Params.Cursor = cursor
		result, err := c.ListTools(ctx, req)
		if err != nil {
			return nil, "", err
		}
		return result.Tools, result.NextCursor, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list tools failed: %w", err)
	}
	out := make([]vmcp.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, vmcp.Tool{
			Name:         t.Name,
			Description:  t.Description,
			InputSchema:  conversion.ConvertToolInputSchema(t.InputSchema),
			OutputSchema: conversion.ConvertToolOutputSchema(t.OutputSchema),
			Annotations:  conversion.ConvertToolAnnotations(t.Annotations),
			BackendID:    target.WorkloadID,
		})
	}
	return out, nil
}

// queryBackendResources lists the backend's resources with cursor-following pagination.
// A backend that advertises the resources capability but does not implement
// resources/list (JSON-RPC -32601, e.g. Atlassian Rovo, see #5231) is tolerated: it
// returns no resources instead of failing the whole discovery. HTTP-level method absence
// remains fatal.
func queryBackendResources(ctx context.Context, c *mcpclient.Client, target *vmcp.BackendTarget) ([]vmcp.Resource, error) {
	resources, err := pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Resource, mcp.Cursor, error) {
		req := mcp.ListResourcesRequest{}
		req.Params.Cursor = cursor
		result, err := c.ListResources(ctx, req)
		if err != nil {
			return nil, "", err
		}
		return result.Resources, result.NextCursor, nil
	})
	if errors.Is(err, mcp.ErrMethodNotFound) {
		slog.Warn("backend advertised resources capability but does not implement resources/list",
			"backendID", target.WorkloadID, "name", target.WorkloadName, "baseURL", target.BaseURL, "method", "resources/list")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list resources failed: %w", err)
	}
	out := make([]vmcp.Resource, 0, len(resources))
	for _, r := range resources {
		out = append(out, vmcp.Resource{
			URI:         r.URI,
			Name:        r.Name,
			Description: r.Description,
			MimeType:    r.MIMEType,
			BackendID:   target.WorkloadID,
		})
	}
	return out, nil
}

// queryBackendPrompts lists the backend's prompts with cursor-following pagination.
// Like queryBackendResources, it tolerates a -32601 from a backend that advertises the
// prompts capability without implementing prompts/list.
func queryBackendPrompts(ctx context.Context, c *mcpclient.Client, target *vmcp.BackendTarget) ([]vmcp.Prompt, error) {
	prompts, err := pagination.ListAll(ctx, func(ctx context.Context, cursor mcp.Cursor) ([]mcp.Prompt, mcp.Cursor, error) {
		req := mcp.ListPromptsRequest{}
		req.Params.Cursor = cursor
		result, err := c.ListPrompts(ctx, req)
		if err != nil {
			return nil, "", err
		}
		return result.Prompts, result.NextCursor, nil
	})
	if errors.Is(err, mcp.ErrMethodNotFound) {
		slog.Warn("backend advertised prompts capability but does not implement prompts/list",
			"backendID", target.WorkloadID, "name", target.WorkloadName, "baseURL", target.BaseURL, "method", "prompts/list")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list prompts failed: %w", err)
	}
	out := make([]vmcp.Prompt, 0, len(prompts))
	for _, p := range prompts {
		args := make([]vmcp.PromptArgument, len(p.Arguments))
		for j, a := range p.Arguments {
			args[j] = vmcp.PromptArgument{
				Name:        a.Name,
				Description: a.Description,
				Required:    a.Required,
			}
		}
		out = append(out, vmcp.Prompt{
			Name:        p.Name,
			Description: p.Description,
			Arguments:   args,
			BackendID:   target.WorkloadID,
		})
	}
	return out, nil
}

// mergeForwardedHeaders returns a HeaderForwardConfig that combines the static
// backend configuration (base) with any per-request forwarded headers captured
// from the caller's request (see headerforward.CaptureMiddleware).
// Delegates to headerforward.MergeForwardedHeaders, which is the shared
// implementation used by both this connector and the shared backend client
// (pkg/vmcp/client).
func mergeForwardedHeaders(base *vmcp.HeaderForwardConfig, forwarded map[string]string) (*vmcp.HeaderForwardConfig, error) {
	return headerforward.MergeForwardedHeaders(base, forwarded)
}

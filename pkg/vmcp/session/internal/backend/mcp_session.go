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

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
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

// claimInjectionRoundTripper injects authenticated user identity claims as HTTP headers
// so backend MCP servers can identify the user without OAuth token introspection.
//
// Headers injected when identity is present:
//   - X-User-Sub:   the authenticated user's subject claim (Google/OIDC sub)
//   - X-User-Email: the user's email address (if present in token)
//   - X-User-Name:  the user's display name (if present in token)
type claimInjectionRoundTripper struct {
	base     http.RoundTripper
	identity *auth.Identity
}

func (c *claimInjectionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if c.identity.Subject != "" {
		cloned.Header.Set("X-User-Sub", c.identity.Subject)
	}
	if c.identity.Email != "" {
		cloned.Header.Set("X-User-Email", c.identity.Email)
	}
	if c.identity.Name != "" {
		cloned.Header.Set("X-User-Name", c.identity.Name)
	}
	return c.base.RoundTrip(cloned)
}

// Compile-time assertion: mcpSession must implement Session.
var _ Session = (*mcpSession)(nil)

// mcpSession wraps a persistent mark3labs MCP client for one backend.
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
			Meta:      conversion.ToMCPMeta(meta),
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

	result, err := c.client.ReadResource(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: backendURI},
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

	result, err := c.client.GetPrompt(ctx, mcp.GetPromptRequest{
		Params: mcp.GetPromptParams{
			Name:      backendName,
			Arguments: stringArgs,
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
func NewHTTPConnector(registry vmcpauth.OutgoingAuthRegistry) func(
	ctx context.Context,
	target *vmcp.BackendTarget,
	identity *auth.Identity,
	sessionHint string,
) (Session, *vmcp.CapabilityList, error) {
	provider := secrets.NewEnvironmentProvider()
	return func(
		ctx context.Context,
		target *vmcp.BackendTarget,
		identity *auth.Identity,
		sessionHint string,
	) (Session, *vmcp.CapabilityList, error) {
		c, err := createMCPClient(ctx, target, identity, registry, sessionHint, provider)
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
		// Initialize; the mark3labs transport captures it internally and exposes
		// it via GetSessionId(). SSE transports do not assign a session ID, so
		// the field remains empty for those backends.
		var backendSessionID string
		if sh, ok := c.GetTransport().(*mcptransport.StreamableHTTP); ok {
			backendSessionID = sh.GetSessionId()
		}

		return &mcpSession{client: c, target: target, backendSessionID: backendSessionID}, caps, nil
	}
}

// createMCPClient builds and starts a mark3labs MCP client for target.
// The transport is started with context.Background() so its lifetime is bound
// to client.Close(), not to any caller-supplied init context.
// sessionHint, when non-empty, is passed as the initial Mcp-Session-Id for
// streamable-HTTP transports so the backend can resume an existing session.
//
// ctx is used only to resolve secret-backed entries in target.HeaderForward at
// client-creation time; the transport itself is started with context.Background()
// as described above. provider supplies values for those secret-backed headers.
func createMCPClient(
	ctx context.Context,
	target *vmcp.BackendTarget,
	identity *auth.Identity,
	registry vmcpauth.OutgoingAuthRegistry,
	sessionHint string,
	provider secrets.Provider,
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
	//   http.DefaultTransport → authRoundTripper → identityRoundTripper → claimInjectionRoundTripper → headerForwardRoundTripper
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
	// Inject user identity as HTTP headers so backend MCP servers can read
	// X-User-Sub / X-User-Email without needing their own /introspect calls.
	if identity != nil {
		base = &claimInjectionRoundTripper{base: base, identity: identity}
	}
	base, err = headerforward.BuildHeaderForwardTripper(ctx, base, target.HeaderForward, provider, target.WorkloadID)
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
		// context.WithTimeout so the mark3labs transport surfaces a descriptive
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
		toolsResult, listErr := c.ListTools(ctx, mcp.ListToolsRequest{})
		if listErr != nil {
			return nil, fmt.Errorf("list tools failed: %w", listErr)
		}
		for _, t := range toolsResult.Tools {
			caps.Tools = append(caps.Tools, vmcp.Tool{
				Name:         t.Name,
				Description:  t.Description,
				InputSchema:  conversion.ConvertToolInputSchema(t.InputSchema),
				OutputSchema: conversion.ConvertToolOutputSchema(t.OutputSchema),
				Annotations:  conversion.ConvertToolAnnotations(t.Annotations),
				BackendID:    target.WorkloadID,
			})
		}
	}

	if serverCaps.Resources != nil {
		resResult, listErr := c.ListResources(ctx, mcp.ListResourcesRequest{})
		switch {
		case errors.Is(listErr, mcp.ErrMethodNotFound):
			// Tolerate JSON-RPC -32601 here so a backend that advertises the
			// resources capability but does not implement resources/list (e.g.
			// Atlassian Rovo, see #5231) still contributes its tools instead of
			// being dropped. HTTP-level method absence is intentionally fatal.
			slog.Warn("backend advertised resources capability but does not implement resources/list",
				"backendID", target.WorkloadID,
				"name", target.WorkloadName,
				"baseURL", target.BaseURL,
				"method", "resources/list",
			)
		case listErr != nil:
			return nil, fmt.Errorf("list resources failed: %w", listErr)
		default:
			for _, r := range resResult.Resources {
				caps.Resources = append(caps.Resources, vmcp.Resource{
					URI:         r.URI,
					Name:        r.Name,
					Description: r.Description,
					MimeType:    r.MIMEType,
					BackendID:   target.WorkloadID,
				})
			}
		}
	}

	if serverCaps.Prompts != nil {
		promptsResult, listErr := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
		switch {
		case errors.Is(listErr, mcp.ErrMethodNotFound):
			// Tolerate JSON-RPC -32601 here so a backend that advertises the
			// prompts capability but does not implement prompts/list (e.g.
			// Atlassian Rovo, see #5231) still contributes its tools instead of
			// being dropped. HTTP-level method absence is intentionally fatal.
			slog.Warn("backend advertised prompts capability but does not implement prompts/list",
				"backendID", target.WorkloadID,
				"name", target.WorkloadName,
				"baseURL", target.BaseURL,
				"method", "prompts/list",
			)
		case listErr != nil:
			return nil, fmt.Errorf("list prompts failed: %w", listErr)
		default:
			for _, p := range promptsResult.Prompts {
				args := make([]vmcp.PromptArgument, len(p.Arguments))
				for j, a := range p.Arguments {
					args[j] = vmcp.PromptArgument{
						Name:        a.Name,
						Description: a.Description,
						Required:    a.Required,
					}
				}
				caps.Prompts = append(caps.Prompts, vmcp.Prompt{
					Name:        p.Name,
					Description: p.Description,
					Arguments:   args,
					BackendID:   target.WorkloadID,
				})
			}
		}
	}

	slog.Debug("Backend capabilities",
		"backendID", target.WorkloadID,
		"tools", len(caps.Tools),
		"resources", len(caps.Resources),
		"prompts", len(caps.Prompts),
	)

	return caps, nil
}

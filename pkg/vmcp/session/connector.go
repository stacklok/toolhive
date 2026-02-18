// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

const (
	// maxBackendResponseSize caps each HTTP response body for streamable-HTTP
	// backends to prevent memory exhaustion. Not applied to SSE transports —
	// see createSessionMCPClient for the rationale.
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

// identityRoundTripper propagates the caller's identity to outgoing backend requests.
type identityRoundTripper struct {
	base     http.RoundTripper
	identity *auth.Identity
}

func (i *identityRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if i.identity != nil {
		ctx := auth.WithIdentity(req.Context(), i.identity)
		req = req.Clone(ctx)
	}
	return i.base.RoundTrip(req)
}

// mcpConnectedBackend wraps a persistent mark3labs MCP client for one backend.
// It is created once per backend during MakeSession and closed when the session ends.
//
// Phase 1 limitation — no reconnection: if the underlying transport drops
// (network error, server restart, SSE stream EOF), all subsequent operations
// on this backend will fail with the transport error. The session must be
// closed and a new one created to reconnect. This affects SSE backends more
// visibly because SSE uses a single long-lived HTTP stream; streamable-HTTP
// backends open a new connection per request and are therefore more resilient.
type mcpConnectedBackend struct {
	client           *mcpclient.Client
	backendSessionID string // backend-assigned session ID (may be empty)
}

func (c *mcpConnectedBackend) sessionID() string { return c.backendSessionID }

func (c *mcpConnectedBackend) close() error { return c.client.Close() }

func (c *mcpConnectedBackend) callTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	backendName := target.GetBackendCapabilityName(toolName)
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
		return nil, fmt.Errorf("tool %q call failed on backend %s: %w", toolName, target.WorkloadID, err)
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

func (c *mcpConnectedBackend) readResource(
	ctx context.Context,
	target *vmcp.BackendTarget,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	backendURI := target.GetBackendCapabilityName(uri)
	if backendURI != uri {
		slog.Debug("Translating resource URI", "clientURI", uri, "backendURI", backendURI)
	}

	result, err := c.client.ReadResource(ctx, mcp.ReadResourceRequest{
		Params: mcp.ReadResourceParams{URI: backendURI},
	})
	if err != nil {
		return nil, fmt.Errorf("resource %q read failed on backend %s: %w", uri, target.WorkloadID, err)
	}

	data, mimeType := conversion.ConcatenateResourceContents(result.Contents)

	return &vmcp.ResourceReadResult{
		Contents: data,
		MimeType: mimeType,
		Meta:     conversion.FromMCPMeta(result.Meta),
	}, nil
}

func (c *mcpConnectedBackend) getPrompt(
	ctx context.Context,
	target *vmcp.BackendTarget,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	backendName := target.GetBackendCapabilityName(name)
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
		return nil, fmt.Errorf("prompt %q get failed on backend %s: %w", name, target.WorkloadID, err)
	}

	// NOTE: ConvertPromptMessages is lossy — non-text content (images, audio)
	// is discarded. Phase 1 limitation; see vmcp.PromptGetResult.
	return &vmcp.PromptGetResult{
		Messages:    conversion.ConvertPromptMessages(result.Messages),
		Description: result.Description,
		Meta:        conversion.FromMCPMeta(result.Meta),
	}, nil
}

// NewHTTPBackendConnector returns a BackendConnector that creates HTTP-based
// (streamable-HTTP or SSE) persistent MCP clients for each backend.
//
// registry provides the authentication strategy for outgoing backend requests.
// Pass a registry configured with the "unauthenticated" strategy to disable auth.
func NewHTTPBackendConnector(registry vmcpauth.OutgoingAuthRegistry) BackendConnector {
	return func(
		ctx context.Context,
		target *vmcp.BackendTarget,
		identity *auth.Identity,
	) (connectedBackend, *vmcp.CapabilityList, error) {
		c, err := createSessionMCPClient(target, identity, registry)
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

		return &mcpConnectedBackend{client: c, backendSessionID: backendSessionID}, caps, nil
	}
}

// createSessionMCPClient builds and starts a mark3labs MCP client for target.
// The transport is started with context.Background() so its lifetime is bound
// to client.Close(), not to any caller-supplied init context.
func createSessionMCPClient(
	target *vmcp.BackendTarget,
	identity *auth.Identity,
	registry vmcpauth.OutgoingAuthRegistry,
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

	// Build shared transport chain: auth → identity propagation.
	// The per-transport sections below may add a size-limiting wrapper on top.
	base := http.RoundTripper(http.DefaultTransport)
	base = &authRoundTripper{
		base:         base,
		authStrategy: strategy,
		authConfig:   target.AuthConfig,
		target:       target,
	}
	base = &identityRoundTripper{base: base, identity: identity}

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
		c, err = mcpclient.NewStreamableHttpClient(
			target.BaseURL,
			mcptransport.WithHTTPTimeout(defaultBackendRequestTimeout),
			mcptransport.WithHTTPBasicClient(httpClient),
		)
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
				Name:        t.Name,
				Description: t.Description,
				InputSchema: conversion.ConvertToolInputSchema(t.InputSchema),
				BackendID:   target.WorkloadID,
			})
		}
	}

	if serverCaps.Resources != nil {
		resResult, listErr := c.ListResources(ctx, mcp.ListResourcesRequest{})
		if listErr != nil {
			return nil, fmt.Errorf("list resources failed: %w", listErr)
		}
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

	if serverCaps.Prompts != nil {
		promptsResult, listErr := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
		if listErr != nil {
			return nil, fmt.Errorf("list prompts failed: %w", listErr)
		}
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

	slog.Debug("Backend capabilities",
		"backendID", target.WorkloadID,
		"tools", len(caps.Tools),
		"resources", len(caps.Resources),
		"prompts", len(caps.Prompts),
	)

	return caps, nil
}

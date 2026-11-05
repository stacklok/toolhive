// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides shared MCP client creation and initialization logic
// used by both the CLI and TUI.
//
// It wraps the mcp-go SDK client constructors and handles transport selection,
// auto-detection (streamable-http with SSE fallback), and the MCP initialize
// handshake using the ToolHive version reported by [versions.GetVersionInfo].
package client

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	mcpclient "github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/versions"
)

// TransportAuto is the sentinel value that triggers auto-detection of the
// transport type (try streamable-http first, then fall back to SSE).
const TransportAuto = "auto"

// ProbeResult contains the MCP metadata returned by initialize.
type ProbeResult struct {
	ProtocolVersion string                 `json:"protocol_version"`
	ServerInfo      mcp.Implementation     `json:"server_info"`
	Capabilities    mcp.ServerCapabilities `json:"capabilities"`
	Transport       string                 `json:"transport"`
}

// Probe performs only the MCP initialize handshake and closes the connection.
// It is intended for readiness checks and CI preflight; it does not enumerate
// or invoke any server capabilities.
func Probe(ctx context.Context, serverURL, transport, clientName string) (*ProbeResult, error) {
	if transport == TransportAuto {
		for _, candidate := range []string{string(types.TransportTypeStreamableHTTP), string(types.TransportTypeSSE)} {
			result, err := probeTransport(ctx, serverURL, candidate, clientName)
			if err == nil {
				return result, nil
			}
			slog.Debug("MCP probe transport failed", "transport", candidate, "error", err)
		}
		return nil, fmt.Errorf("MCP initialize failed for streamable-http and SSE")
	}
	return probeTransport(ctx, serverURL, transport, clientName)
}

func probeTransport(ctx context.Context, serverURL, transport, clientName string) (*ProbeResult, error) {
	c, err := newClient(serverURL, transport)
	if err != nil {
		return nil, err
	}
	result, err := startAndInitializeResult(ctx, c, clientName)
	if closeErr := c.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("close MCP client after probe: %w", closeErr)
	}
	if err != nil {
		return nil, err
	}
	return &ProbeResult{
		ProtocolVersion: result.ProtocolVersion,
		ServerInfo:      result.ServerInfo,
		Capabilities:    result.Capabilities,
		Transport:       string(resolveTransport(serverURL, transport)),
	}, nil
}

// Connect creates an MCP SDK client for the given serverURL and transport,
// starts the underlying transport, and performs the MCP initialize handshake.
//
// The clientName is included in the ClientInfo sent during initialization
// (e.g. "toolhive-cli" or "toolhive-tui").
//
// transport must be one of:
//   - [TransportAuto] -- try streamable-http, fall back to SSE
//   - "sse"
//   - "streamable-http"
//
// The returned client is fully connected and ready for use. The caller is
// responsible for calling Close when done.
func Connect(ctx context.Context, serverURL, transport, clientName string) (*mcpclient.Client, error) {
	if transport == TransportAuto {
		return connectWithAutoDetect(ctx, serverURL, clientName)
	}
	c, err := newClient(serverURL, transport)
	if err != nil {
		return nil, err
	}
	if err := startAndInitialize(ctx, c, clientName); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// newClient constructs an MCP SDK client for the given serverURL and explicit
// transport type. The client is not yet started or initialized.
func newClient(serverURL, transport string) (*mcpclient.Client, error) {
	tt := resolveTransport(serverURL, transport)
	switch tt {
	case types.TransportTypeSSE:
		c, err := mcpclient.NewSSEMCPClient(serverURL)
		if err != nil {
			return nil, fmt.Errorf("create SSE MCP client: %w", err)
		}
		return c, nil
	case types.TransportTypeStreamableHTTP:
		c, err := mcpclient.NewStreamableHttpClient(serverURL)
		if err != nil {
			return nil, fmt.Errorf("create streamable-http MCP client: %w", err)
		}
		return c, nil
	case types.TransportTypeStdio:
		return nil, fmt.Errorf("stdio transport is not supported for MCP client connections")
	case types.TransportTypeInspector:
		return nil, fmt.Errorf("inspector transport is not supported for MCP client connections")
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", tt)
	}
}

// connectWithAutoDetect tries streamable-http first, then falls back to SSE.
// The returned client is fully initialized.
func connectWithAutoDetect(ctx context.Context, serverURL, clientName string) (*mcpclient.Client, error) {
	slog.Debug("trying streamable-http transport", "url", serverURL)
	streamableClient, err := mcpclient.NewStreamableHttpClient(serverURL)
	if err == nil {
		if err := startAndInitialize(ctx, streamableClient, clientName); err == nil {
			slog.Debug("connected using streamable-http transport")
			return streamableClient, nil
		}
		_ = streamableClient.Close()
		slog.Debug("streamable-http transport failed, trying SSE fallback")
	}

	slog.Debug("trying SSE transport", "url", serverURL)
	sseClient, err := mcpclient.NewSSEMCPClient(serverURL)
	if err != nil {
		return nil, fmt.Errorf("create MCP client (tried streamable-http and SSE): %w", err)
	}
	if err := startAndInitialize(ctx, sseClient, clientName); err != nil {
		_ = sseClient.Close()
		return nil, fmt.Errorf("connect using both streamable-http and SSE transports: %w", err)
	}
	slog.Debug("connected using SSE transport")
	return sseClient, nil
}

// startAndInitialize starts the transport and performs the MCP initialize
// handshake using the ToolHive version.
func startAndInitialize(ctx context.Context, c *mcpclient.Client, clientName string) error {
	_, err := startAndInitializeResult(ctx, c, clientName)
	return err
}

func startAndInitializeResult(ctx context.Context, c *mcpclient.Client, clientName string) (*mcp.InitializeResult, error) {
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("start MCP client: %w", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.Capabilities = mcp.ClientCapabilities{}
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    clientName,
		Version: versions.GetVersionInfo().Version,
	}
	result, err := c.Initialize(ctx, initReq)
	if err != nil {
		return nil, fmt.Errorf("initialize MCP client: %w", err)
	}
	return result, nil
}

// resolveTransport determines the transport type from the user-supplied value
// and the URL path. When the value is not a recognized transport string the
// function falls back to URL-based heuristics.
func resolveTransport(serverURL, transport string) types.TransportType {
	switch transport {
	case string(types.TransportTypeSSE):
		return types.TransportTypeSSE
	case string(types.TransportTypeStreamableHTTP):
		return types.TransportTypeStreamableHTTP
	}

	// Infer from URL path.
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		slog.Warn("failed to parse server URL, defaulting to streamable-http",
			"url", serverURL, "error", err)
		return types.TransportTypeStreamableHTTP
	}

	path := parsedURL.Path
	if strings.HasSuffix(path, "/"+streamable.HTTPStreamableHTTPEndpoint) ||
		strings.HasSuffix(path, streamable.HTTPStreamableHTTPEndpoint) {
		return types.TransportTypeStreamableHTTP
	}
	if strings.HasSuffix(path, ssecommon.HTTPSSEEndpoint) {
		return types.TransportTypeSSE
	}

	// Default to streamable-http (SSE is deprecated).
	return types.TransportTypeStreamableHTTP
}

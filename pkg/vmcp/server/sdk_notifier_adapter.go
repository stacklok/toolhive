// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// sdkNotifierAdapter wraps mcpcompat MCPServer to implement vmcp.ClientNotifier.
//
// It is the sole point where the SDK's SendNotificationToClient appears: it
// translates the domain ProgressNotification/LogMessage into the SDK's
// (method, params) form and delegates to the server, keeping the backend client
// free of SDK coupling. It mirrors sdkElicitationAdapter.
//
// Forwarding is best-effort: when the context carries no downstream session
// (health probes, capability listing) the SDK returns ErrNoActiveSession, which
// the adapter swallows so the backend's notification is silently dropped rather
// than surfaced as an error.
//
// Thread-safety: Safe for concurrent calls. The mcpcompat MCPServer is thread-safe.
type sdkNotifierAdapter struct {
	// mcpServer is the mcpcompat SDK server. Typed as the minimal mcpClientNotifier
	// seam so the translation logic can be unit-tested against a fake SDK;
	// *server.MCPServer satisfies it.
	mcpServer mcpClientNotifier
}

// mcpClientNotifier is the minimal slice of the mcpcompat SDK that the adapter
// depends on. *server.MCPServer satisfies it in production; tests substitute a
// fake to verify domain -> mcp-go translation without a live session.
type mcpClientNotifier interface {
	SendNotificationToClient(ctx context.Context, method string, params map[string]any) error
}

// NewSDKNotifierAdapter creates a notifier adapter that wraps the mcpcompat SDK
// server. The returned adapter implements vmcp.ClientNotifier by translating the
// domain notification into the SDK's (method, params) form and delegating to
// SendNotificationToClient. Pass the *server.MCPServer obtained from
// (*Server).MCPServer() so notifications correlate with the server handling
// incoming client sessions.
func NewSDKNotifierAdapter(mcpServer *server.MCPServer) vmcp.ClientNotifier {
	return &sdkNotifierAdapter{mcpServer: mcpServer}
}

// NotifyProgress forwards a notifications/progress message to the downstream
// client. Total and Message are omitted from the wire params when unset (zero /
// empty) so a backend that sends only a progress value does not emit a spurious
// total:0. Best-effort: a missing downstream session is not an error.
func (a *sdkNotifierAdapter) NotifyProgress(ctx context.Context, n vmcp.ProgressNotification) error {
	params := map[string]any{
		"progressToken": n.ProgressToken,
		"progress":      n.Progress,
	}
	if n.Total != 0 {
		params["total"] = n.Total
	}
	if n.Message != "" {
		params["message"] = n.Message
	}
	return a.send(ctx, vmcp.MethodProgressNotification, params)
}

// NotifyLog forwards a notifications/message (logging) message to the downstream
// client. Logger is omitted when empty. Best-effort: a missing downstream
// session is not an error.
func (a *sdkNotifierAdapter) NotifyLog(ctx context.Context, n vmcp.LogMessage) error {
	params := map[string]any{
		"level": n.Level,
		"data":  n.Data,
	}
	if n.Logger != "" {
		params["logger"] = n.Logger
	}
	return a.send(ctx, vmcp.MethodLogNotification, params)
}

// send delegates to the SDK and swallows the no-active-session error so
// forwarding stays best-effort per the ClientNotifier contract.
func (a *sdkNotifierAdapter) send(ctx context.Context, method string, params map[string]any) error {
	err := a.mcpServer.SendNotificationToClient(ctx, method, params)
	if errors.Is(err, server.ErrNoActiveSession) {
		return nil
	}
	return err
}

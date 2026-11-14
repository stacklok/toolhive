// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"encoding/json"
	"fmt"

	coremcp "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/test/e2e"
)

// This file holds the era-pinned Legacy (2025-11-25) session primitives shared
// by the specs that assert Legacy-session semantics — Redis-backed cross-pod
// reconstruction, pod-restart recovery, lazy termination eviction,
// upstreamInject identity restore, and the dual-era Redis suite.
//
// They exist because such specs must NOT use CreateInitializedMCPClient (or
// any mcpcompat-built client): mcpcompat wraps go-sdk, go-sdk v1.7's Connect
// is Modern-first — it probes server/discover before initialize and upgrades
// to 2026-07-28 whenever the server advertises it — and the shim cannot pin a
// protocol version (#5911). Against a Modern-serving vMCP such a client
// negotiates Modern, which has no sessions, and every session assertion fails.
// The raw client pins the era explicitly per request instead, which is the
// point: a spec about sessions declares, at each call site, that it depends on
// the Legacy revision (the #6051 convention).
//
// Both helpers return errors rather than asserting, so they are safe inside
// Eventually/Consistently retry loops and on goroutines without GinkgoRecover.

// legacySessionInit performs the Legacy initialize + notifications/initialized
// handshake against url and returns the session ID vMCP assigned. extraHeaders
// (e.g. Authorization for OIDC-protected instances) are applied to every
// request of the handshake.
func legacySessionInit(
	client *e2e.RawMCPClient, url, clientName string, extraHeaders map[string]string,
) (string, error) {
	ctx := context.Background()
	req := e2e.NewLegacyInitializeRequest(clientName, "1.0")
	for k, v := range extraHeaders {
		req.SetHeader(k, v)
	}
	resp, err := client.Send(ctx, url, req)
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("initialize: status %d, body: %s", resp.StatusCode, resp.Body)
	}
	sessionID := resp.Headers.Get(e2e.HeaderMCPSessionID)
	if sessionID == "" {
		return "", fmt.Errorf("initialize did not assign a session id")
	}

	notifyReq, err := e2e.NewLegacyRequest("notifications/initialized", nil)
	if err != nil {
		return "", fmt.Errorf("build notifications/initialized: %w", err)
	}
	// WithID(nil) omits "id" entirely, making this a true JSON-RPC notification.
	notifyReq.WithID(nil).WithSessionID(sessionID).SetHeader(e2e.HeaderMCPProtocolVersion, coremcp.MCPVersionLegacy)
	for k, v := range extraHeaders {
		notifyReq.SetHeader(k, v)
	}
	notifyResp, err := client.Send(ctx, url, notifyReq)
	if err != nil {
		return "", fmt.Errorf("notifications/initialized: %w", err)
	}
	if notifyResp.StatusCode != 202 {
		return "", fmt.Errorf("notifications/initialized: status %d, body: %s", notifyResp.StatusCode, notifyResp.Body)
	}

	return sessionID, nil
}

// legacySessionCallTool sends a Legacy tools/call for toolName on sessionID
// and returns the raw response. It deliberately does NOT inspect
// resp.StatusCode/Error — a non-2xx response is a normal, successfully-received
// HTTP response some callers assert on (e.g. rejection specs); only an actual
// transport failure from Send is surfaced as the returned error. For a plain
// "the call worked and echoed X" assertion, pass the response to
// dualEraEchoErr(resp, wantOutput, "") — the empty resultType is what a Legacy
// client's envelope carries.
func legacySessionCallTool(
	client *e2e.RawMCPClient, url, sessionID, toolName string,
	args map[string]any, extraHeaders map[string]string,
) (*e2e.RawResponse, error) {
	req, err := e2e.NewLegacyRequest("tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return nil, fmt.Errorf("build tools/call: %w", err)
	}
	req.WithSessionID(sessionID).SetHeader(e2e.HeaderMCPProtocolVersion, coremcp.MCPVersionLegacy)
	for k, v := range extraHeaders {
		req.SetHeader(k, v)
	}
	return client.Send(context.Background(), url, req)
}

// legacySessionListTools sends a Legacy tools/list on sessionID and returns
// the aggregated tool names. Sending it to a pod that has never seen sessionID
// is exactly what triggers a Redis-backed session reconstruction there, so the
// cross-pod specs call this against pod B with pod A's session. extraHeaders
// are applied to the request.
func legacySessionListTools(
	client *e2e.RawMCPClient, url, sessionID string, extraHeaders map[string]string,
) ([]string, error) {
	req, err := e2e.NewLegacyRequest("tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("build tools/list: %w", err)
	}
	req.WithSessionID(sessionID).SetHeader(e2e.HeaderMCPProtocolVersion, coremcp.MCPVersionLegacy)
	for k, v := range extraHeaders {
		req.SetHeader(k, v)
	}
	resp, err := client.Send(context.Background(), url, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tools/list: status %d, body: %s", resp.StatusCode, resp.Body)
	}
	// A schema-rejected tools/list comes back as a JSON-RPC error inside a 200
	// (unlike tools/call, where yardstick reports validation failure via
	// isError:true), so this check is meaningful.
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list: JSON-RPC error: %+v", resp.Error)
	}

	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("tools/list: unmarshal result: %w, raw: %s", err, resp.Result)
	}
	names := make([]string, len(result.Tools))
	for i, t := range result.Tools {
		names[i] = t.Name
	}
	return names, nil
}

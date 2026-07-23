// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

// This file hand-rolls the MCP 2026-07-28 ("Modern") stateless response
// envelope: the wire shape go-sdk@v1.7.0-pre.3 produces for a single-shot
// tools/list, resources/list, resources/templates/list, prompts/list,
// tools/call, resources/read, and prompts/get. ToolHive imports go-sdk
// v1.6.1, whose Modern types don't exist (or are unexported), so this is a
// durable parallel serializer, not a stopgap: resultType and _meta.serverInfo
// are set by unexported SDK functions (setCompleteResultType,
// annotateServerInfo) that run inside the exact ServerSession dispatch this
// package bypasses for Modern stateless requests. A future go-sdk bump cannot
// just delete these structs and marshal SDK result types directly -- the
// Modern annotations would vanish with them.
//
// modernResultTypeComplete is the sole value dispatchModern ever needs: this
// package only performs single-shot dispatch, never the elicitation retry
// loop (resultType "input_required"), so every result built here is
// unconditionally "complete". Do not add Legacy-conditional branching to
// these structs -- this file is reached only for Modern requests.
const modernResultTypeComplete = "complete"

// modernServerInfoKey is the go-sdk's MetaKeyServerInfo
// (protocol.go:2367 in go-sdk@v1.7.0-pre.3), reproduced by hand since v1.6.1
// does not export it.
const modernServerInfoKey = "io.modelcontextprotocol/serverInfo"

// modernServerInfo mirrors the go-sdk's Implementation type.
type modernServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// modernMeta carries the _meta.serverInfo entry the SDK attaches to every
// Modern result, unconditionally and together with resultType.
type modernMeta struct {
	ServerInfo modernServerInfo `json:"io.modelcontextprotocol/serverInfo"`
}

func newModernMeta(serverName, serverVersion string) modernMeta {
	return modernMeta{ServerInfo: modernServerInfo{Name: serverName, Version: serverVersion}}
}

// newModernResultMeta builds _meta for tools/call, resources/read, and
// prompts/get, the three builders whose domain result carries its own
// backend Meta (vmcp.ToolCallResult.Meta etc.) -- unlike the four list
// results and server/discover, which have no per-result backend meta and use
// the serverInfo-only newModernMeta above. The SDK path preserves backend
// meta via conversion.ToMCPMeta (serve_handlers.go); dropping it here would
// silently discard whatever the backend attached (progress tokens, trace
// ids, ...). backendMeta is cloned before the serverInfo key is added (copy
// before mutating caller input) so the domain result's map is never touched.
// A Modern backend could in principle return that same namespaced key; the
// unconditional overwrite is still correct here because the client's actual
// MCP peer is vMCP, not the backend, so vMCP's own serverInfo must win.
//
// Every other backendMeta key -- including any other io.modelcontextprotocol/*
// reserved key a backend happens to set -- is forwarded unfiltered, matching
// the Legacy path's conversion.ToMCPMeta. Stripping reserved keys is a
// tracked follow-up; it must land in a helper shared by both paths, not here
// only, or Legacy and Modern would drift.
func newModernResultMeta(backendMeta map[string]any, serverName, serverVersion string) map[string]any {
	meta := maps.Clone(backendMeta)
	if meta == nil {
		meta = make(map[string]any, 1)
	}
	meta[modernServerInfoKey] = modernServerInfo{Name: serverName, Version: serverVersion}
	return meta
}

// modernCacheable mirrors the go-sdk's Cacheable struct (protocol.go:1168),
// with no omitempty: both fields are always present on the wire.
//
// CacheScope is deliberately hardcoded to "private", diverging from the SDK's
// "public" default. The four list results and resources/read vary by caller
// identity (admission.FilterTools/Resources/Prompts, AllowResourceRead), so
// advertising "public" would let a shared cache/intermediary serve one
// identity's admission-filtered view to another. TTLMs is 0 ("immediately
// stale") as an interim value for a re-aggregating gateway with no cache
// invalidation signal yet.
type modernCacheable struct {
	TTLMs      int    `json:"ttlMs"`
	CacheScope string `json:"cacheScope"`
}

func newModernCacheable() modernCacheable {
	return modernCacheable{TTLMs: 0, CacheScope: "private"}
}

// modernToolsListResult is the tools/list wire result.
type modernToolsListResult struct {
	ResultType string `json:"resultType"`
	modernCacheable
	Tools []mcp.Tool `json:"tools"`
	Meta  modernMeta `json:"_meta"`
}

// modernResourcesListResult is the resources/list wire result.
type modernResourcesListResult struct {
	ResultType string `json:"resultType"`
	modernCacheable
	Resources []mcp.Resource `json:"resources"`
	Meta      modernMeta     `json:"_meta"`
}

// modernResourceTemplatesListResult is the resources/templates/list wire result.
type modernResourceTemplatesListResult struct {
	ResultType string `json:"resultType"`
	modernCacheable
	ResourceTemplates []mcp.ResourceTemplate `json:"resourceTemplates"`
	Meta              modernMeta             `json:"_meta"`
}

// modernPromptsListResult is the prompts/list wire result.
type modernPromptsListResult struct {
	ResultType string `json:"resultType"`
	modernCacheable
	Prompts []mcp.Prompt `json:"prompts"`
	Meta    modernMeta   `json:"_meta"`
}

// modernCallToolResult is the tools/call wire result. Unlike the four list
// results and resources/read, it does NOT embed modernCacheable -- a tool
// call is an action, not a cacheable read, and the SDK never attaches
// Cacheable to CallToolResult. Meta is a map (not modernMeta) because it must
// carry both the backend's own result meta AND serverInfo -- see
// newModernResultMeta.
type modernCallToolResult struct {
	ResultType        string         `json:"resultType"`
	Content           []mcp.Content  `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
	Meta              map[string]any `json:"_meta"`
}

// modernReadResourceResult is the resources/read wire result. Meta is a map
// for the same reason as modernCallToolResult -- see newModernResultMeta.
type modernReadResourceResult struct {
	ResultType string `json:"resultType"`
	modernCacheable
	Contents []mcp.ResourceContents `json:"contents"`
	Meta     map[string]any         `json:"_meta"`
}

// modernGetPromptResult is the prompts/get wire result. Like tools/call, it
// does NOT embed modernCacheable -- the SDK never attaches Cacheable to
// GetPromptResult. Meta is a map for the same reason as modernCallToolResult
// -- see newModernResultMeta.
type modernGetPromptResult struct {
	ResultType  string              `json:"resultType"`
	Description string              `json:"description,omitempty"`
	Messages    []mcp.PromptMessage `json:"messages"`
	Meta        map[string]any      `json:"_meta"`
}

// modernCompletionDetails mirrors the go-sdk's CompletionResultDetails
// (protocol.go:653 in go-sdk@v1.7.0-pre.3): Values has no omitempty tag
// there, so it doesn't here either -- an empty result still marshals
// "values":[] rather than omitting the field.
type modernCompletionDetails struct {
	Values  []string `json:"values"`
	Total   int      `json:"total,omitempty"`
	HasMore bool     `json:"hasMore,omitempty"`
}

// modernCompleteResult is the completion/complete wire result. Like
// tools/call and prompts/get, it does NOT embed modernCacheable -- the SDK
// never attaches Cacheable to CompleteResult. Meta is modernMeta (not a map)
// because vmcp.CompletionResult carries no backend meta to preserve -- unlike
// newModernResultMeta's callers, there is nothing to clone here.
type modernCompleteResult struct {
	ResultType string                  `json:"resultType"`
	Completion modernCompletionDetails `json:"completion"`
	Meta       modernMeta              `json:"_meta"`
}

// newModernComplete builds the completion/complete wire result from the
// core's CompletionResult. Values is initialized to a non-nil slice so an
// empty result marshals as [] not null, matching the four list builders.
//
// result is expected non-nil (core.Complete never returns a nil result
// without an error); the nil check below is defense-in-depth against a
// plausible aggregator bug, not an expected input.
func newModernComplete(result *vmcp.CompletionResult, serverName, serverVersion string) modernCompleteResult {
	if result == nil {
		result = &vmcp.CompletionResult{}
	}
	values := result.Values
	if values == nil {
		values = []string{}
	}
	return modernCompleteResult{
		ResultType: modernResultTypeComplete,
		Completion: modernCompletionDetails{
			Values:  values,
			Total:   result.Total,
			HasMore: result.HasMore,
		},
		Meta: newModernMeta(serverName, serverVersion),
	}
}

// modernDiscoverResult is the server/discover wire result -- Modern's
// replacement for initialize+capability negotiation. Unlike the four list
// results, it carries no descriptor arrays: Capabilities reuses mcpcompat's
// mcp.ServerCapabilities, whose per-feature fields are themselves the "flag"
// (a pointer field present iff the identity can reach that feature at all).
type modernDiscoverResult struct {
	ResultType string `json:"resultType"`
	modernCacheable
	SupportedVersions []string               `json:"supportedVersions"`
	Capabilities      mcp.ServerCapabilities `json:"capabilities"`
	Meta              modernMeta             `json:"_meta"`
}

// newModernDiscover builds the server/discover wire result. hasTools/
// hasResources/hasPrompts are the caller's len(core.List*(ctx, identity))>0
// admission-filtered presence checks -- this function only shapes them into
// the wire capability flags, so a discover response reflects exclusively
// what this identity may reach, same as the four list results. hasTemplates
// folds into the "resources" capability: the wire protocol has no separate
// resource-templates capability flag.
//
// Completions is set unconditionally, unlike the three identity-filtered
// flags above: completion/complete has no per-identity admission of its own
// (core.Complete authorizes the underlying prompt/resource ref at dispatch,
// not the completions feature itself), and this dispatcher serves it for
// every caller once Modern dispatch is enabled at all -- matching how the
// Legacy/SDK path always wires server.WithCompletionHandler regardless of
// identity (serve.go).
//
// SupportedVersions enumerates every protocol version this vMCP endpoint
// actually serves, not just Modern: mcpparser.MCPVersionModern (this
// dispatcher) plus mcp.LATEST_PROTOCOL_VERSION, the one Legacy version the
// SDK path negotiates (mcpcompat's handleInitialize always responds with
// LATEST_PROTOCOL_VERSION regardless of what a client requests -- it does
// not support older Legacy revisions either). A discover-first client uses
// this list to decide whether it can skip the Legacy initialize handshake.
func newModernDiscover(
	hasTools, hasResources, hasTemplates, hasPrompts bool, serverName, serverVersion string,
) modernDiscoverResult {
	var caps mcp.ServerCapabilities
	if hasTools {
		caps.Tools = &struct {
			ListChanged bool `json:"listChanged,omitempty"`
		}{}
	}
	if hasResources || hasTemplates {
		// Subscribe is left false (omitted on the wire): this stateless
		// single-shot dispatcher has no persistent connection to a client to
		// push a server-initiated resources/updated notification over, so
		// resources/subscribe is not advertised here and returns -32601 by
		// design, not by oversight.
		caps.Resources = &struct {
			Subscribe   bool `json:"subscribe,omitempty"`
			ListChanged bool `json:"listChanged,omitempty"`
		}{}
	}
	if hasPrompts {
		caps.Prompts = &struct {
			ListChanged bool `json:"listChanged,omitempty"`
		}{}
	}
	caps.Completions = &struct{}{}
	return modernDiscoverResult{
		ResultType:        modernResultTypeComplete,
		modernCacheable:   newModernCacheable(),
		SupportedVersions: []string{mcpparser.MCPVersionModern, mcp.LATEST_PROTOCOL_VERSION},
		Capabilities:      caps,
		Meta:              newModernMeta(serverName, serverVersion),
	}
}

// newModernToolsList builds the tools/list wire result from the core's
// admission-filtered domain tools.
func newModernToolsList(tools []vmcp.Tool, serverName, serverVersion string) (modernToolsListResult, error) {
	wireTools := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		wireTool, err := modernToolFromDomain(t)
		if err != nil {
			return modernToolsListResult{}, err
		}
		wireTools = append(wireTools, wireTool)
	}
	return modernToolsListResult{
		ResultType:      modernResultTypeComplete,
		modernCacheable: newModernCacheable(),
		Tools:           wireTools,
		Meta:            newModernMeta(serverName, serverVersion),
	}, nil
}

// newModernResourcesList builds the resources/list wire result from the
// core's admission-filtered domain resources.
func newModernResourcesList(resources []vmcp.Resource, serverName, serverVersion string) modernResourcesListResult {
	wireResources := make([]mcp.Resource, 0, len(resources))
	for _, r := range resources {
		wireResources = append(wireResources, modernResourceFromDomain(r))
	}
	return modernResourcesListResult{
		ResultType:      modernResultTypeComplete,
		modernCacheable: newModernCacheable(),
		Resources:       wireResources,
		Meta:            newModernMeta(serverName, serverVersion),
	}
}

// newModernResourceTemplatesList builds the resources/templates/list wire
// result from the core's admission-filtered domain resource templates.
func newModernResourceTemplatesList(
	templates []vmcp.ResourceTemplate, serverName, serverVersion string,
) modernResourceTemplatesListResult {
	wireTemplates := make([]mcp.ResourceTemplate, 0, len(templates))
	for _, t := range templates {
		wireTemplates = append(wireTemplates, modernResourceTemplateFromDomain(t))
	}
	return modernResourceTemplatesListResult{
		ResultType:        modernResultTypeComplete,
		modernCacheable:   newModernCacheable(),
		ResourceTemplates: wireTemplates,
		Meta:              newModernMeta(serverName, serverVersion),
	}
}

// newModernPromptsList builds the prompts/list wire result from the core's
// admission-filtered domain prompts.
func newModernPromptsList(prompts []vmcp.Prompt, serverName, serverVersion string) modernPromptsListResult {
	wirePrompts := make([]mcp.Prompt, 0, len(prompts))
	for _, p := range prompts {
		wirePrompts = append(wirePrompts, modernPromptFromDomain(p))
	}
	return modernPromptsListResult{
		ResultType:      modernResultTypeComplete,
		modernCacheable: newModernCacheable(),
		Prompts:         wirePrompts,
		Meta:            newModernMeta(serverName, serverVersion),
	}
}

// newModernCallToolResult builds the tools/call wire result from the core's
// ToolCallResult. StructuredContent is omitted entirely (not merely
// omitempty-false) when the core did not set it, matching the SDK's
// omitempty behavior.
//
// result is expected non-nil (core.CallTool never returns a nil result
// without an error); the nil check below is defense-in-depth against a
// plausible aggregator bug, not an expected input.
func newModernCallToolResult(result *vmcp.ToolCallResult, serverName, serverVersion string) modernCallToolResult {
	if result == nil {
		result = &vmcp.ToolCallResult{}
	}
	var structuredContent any
	if len(result.StructuredContent) > 0 {
		structuredContent = result.StructuredContent
	}
	return modernCallToolResult{
		ResultType:        modernResultTypeComplete,
		Content:           conversion.ToMCPContents(result.Content),
		StructuredContent: structuredContent,
		IsError:           result.IsError,
		Meta:              newModernResultMeta(result.Meta, serverName, serverVersion),
	}
}

// newModernReadResourceResult builds the resources/read wire result from the
// core's ResourceReadResult.
//
// result is expected non-nil (core.ReadResource never returns a nil result
// without an error); the nil check below is defense-in-depth against a
// plausible aggregator bug, not an expected input.
func newModernReadResourceResult(
	result *vmcp.ResourceReadResult, serverName, serverVersion string,
) modernReadResourceResult {
	if result == nil {
		result = &vmcp.ResourceReadResult{}
	}
	return modernReadResourceResult{
		ResultType:      modernResultTypeComplete,
		modernCacheable: newModernCacheable(),
		Contents:        conversion.ToMCPResourceContents(result.Contents),
		Meta:            newModernResultMeta(result.Meta, serverName, serverVersion),
	}
}

// newModernGetPromptResult builds the prompts/get wire result from the
// core's PromptGetResult.
//
// result is expected non-nil (core.GetPrompt never returns a nil result
// without an error); the nil check below is defense-in-depth against a
// plausible aggregator bug, not an expected input.
func newModernGetPromptResult(result *vmcp.PromptGetResult, serverName, serverVersion string) modernGetPromptResult {
	if result == nil {
		result = &vmcp.PromptGetResult{}
	}
	return modernGetPromptResult{
		ResultType:  modernResultTypeComplete,
		Description: result.Description,
		Messages:    conversion.ToMCPPromptMessages(result.Messages),
		Meta:        newModernResultMeta(result.Meta, serverName, serverVersion),
	}
}

// modernToolFromDomain converts a vmcp.Tool to the wire mcp.Tool used in a
// tools/list result. Mirrors coreSessionTools' Legacy adaptation
// (serve_handlers.go) field-for-field, so Legacy and Modern report identical
// tool shapes -- do not reinvent this mapping if the domain type changes.
func modernToolFromDomain(t vmcp.Tool) (mcp.Tool, error) {
	schemaJSON, err := json.Marshal(t.InputSchema)
	if err != nil {
		return mcp.Tool{}, fmt.Errorf("marshal input schema for tool %s: %w", t.Name, err)
	}
	wireTool := mcp.Tool{
		Name:           t.Name,
		Description:    t.Description,
		RawInputSchema: schemaJSON,
		// ponytail: a tool with no annotations still marshals "annotations":{}
		// because mcpcompat's Tool.MarshalJSON (mcpcompat/mcp/tools.go:343)
		// writes the field unconditionally. Shared with Legacy's
		// coreSessionTools, so the real fix belongs in mcpcompat, not here --
		// fixing it only in this file would break Legacy/Modern parity.
		Annotations: conversion.ToMCPToolAnnotations(t.Annotations),
	}
	// Unlike the required InputSchema above, OutputSchema is best-effort: on
	// failure the tool is still advertised without it (matches
	// coreSessionTools).
	if t.OutputSchema != nil {
		if outputSchemaJSON, marshalErr := json.Marshal(t.OutputSchema); marshalErr != nil {
			slog.Warn("failed to marshal tool output schema", "tool", t.Name, "error", marshalErr)
		} else {
			wireTool.RawOutputSchema = outputSchemaJSON
		}
	}
	return wireTool, nil
}

// modernResourceFromDomain converts a vmcp.Resource to the wire mcp.Resource
// used in a resources/list result.
func modernResourceFromDomain(r vmcp.Resource) mcp.Resource {
	return mcp.Resource{
		Name:        r.Name,
		URI:         r.URI,
		Description: r.Description,
		MIMEType:    r.MimeType,
	}
}

// modernResourceTemplateFromDomain converts a vmcp.ResourceTemplate to the
// wire mcp.ResourceTemplate used in a resources/templates/list result.
func modernResourceTemplateFromDomain(t vmcp.ResourceTemplate) mcp.ResourceTemplate {
	return mcp.ResourceTemplate{
		Name:        t.Name,
		URITemplate: t.URITemplate,
		Description: t.Description,
		MIMEType:    t.MimeType,
	}
}

// modernPromptFromDomain converts a vmcp.Prompt to the wire mcp.Prompt used
// in a prompts/list result.
func modernPromptFromDomain(p vmcp.Prompt) mcp.Prompt {
	arguments := make([]mcp.PromptArgument, 0, len(p.Arguments))
	for _, arg := range p.Arguments {
		arguments = append(arguments, mcp.PromptArgument{
			Name:        arg.Name,
			Description: arg.Description,
			Required:    arg.Required,
		})
	}
	return mcp.Prompt{
		Name:        p.Name,
		Description: p.Description,
		Arguments:   arguments,
	}
}

// writeModernResult writes the JSON-RPC success envelope
// {"jsonrpc":"2.0","id":<id>,"result":<result>} as a single HTTP 200
// application/json response -- never SSE, no Mcp-Session-Id -- matching the
// Modern stateless wire contract.
func writeModernResult(w http.ResponseWriter, id, result any) {
	writeModernEnvelope(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

// writeModernError writes a JSON-RPC error envelope, deriving the HTTP
// status from the JSON-RPC code -- mirroring go-sdk's extractErrorStatus
// (streamable.go:1033-1061):
//   - -32601 (method not found) -> 404: the 2026-07-28 spec MUSTs 404 for an
//     unimplemented method.
//   - -32602 (invalid params) -> 400: matches the SDK's own mapping.
//   - everything else (-32600 batch, -32603 internal) -> 200: matches
//     extractErrorStatus returning 0 for those codes -- the request was
//     accepted and processed, so the failure is an application-level
//     JSON-RPC error riding the transport, not a wire-level rejection.
//
// This is a protocol-level mapping, unrelated to authorization: only
// writeModernDenied changes status for a POLICY reason (403).
func writeModernError(w http.ResponseWriter, id any, code int, msg string) {
	status := http.StatusOK
	switch code {
	case jsonRPCCodeMethodNotFound:
		status = http.StatusNotFound
	case jsonRPCCodeInvalidParams:
		status = http.StatusBadRequest
	}
	writeModernEnvelope(w, status, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": msg,
		},
	})
}

// writeModernDenied writes a JSON-RPC error envelope at HTTP 403 with
// mcpparser.JSONRPCCodeDenied, mirroring the Legacy call gate
// (call_gate.go) and pkg/authz.handleUnauthorized: the 403 status is what
// makes the audit middleware log the request as denied rather than failed.
func writeModernDenied(w http.ResponseWriter, id any, msg string) {
	writeModernEnvelope(w, http.StatusForbidden, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    mcpparser.JSONRPCCodeDenied,
			"message": msg,
		},
	})
}

// writeModernEnvelope marshals envelope before writing headers/status, so a
// marshal failure never leaves a response half-written (mirrors
// classificationErrorBody's build-then-write ordering).
func writeModernEnvelope(w http.ResponseWriter, status int, envelope map[string]any) {
	body, err := json.Marshal(envelope)
	if err != nil {
		// Unreachable in practice: every value passed to these writers is a
		// wire struct defined in this file, a domain string, or an int code --
		// all JSON-marshalable. Fall back to a valid JSON-RPC error body
		// rather than writing nothing.
		slog.Error("failed to marshal Modern JSON-RPC envelope", "error", err)
		body = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"Internal error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	// Belt-and-suspenders behind the JSON-level cacheScope:"private" hint:
	// an MCP-unaware intermediary (CDN, corporate proxy) wouldn't look at the
	// body, so every Modern response also asserts it at the HTTP layer.
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	//nolint:gosec // G104: writing a JSON-RPC response to an HTTP client
	_, _ = w.Write(body)
}

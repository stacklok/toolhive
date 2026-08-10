// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import "github.com/stacklok/toolhive-core/mcpcompat/mcp"

const (
	// methodLabelUnknown marks a request whose MCP method could not be parsed.
	// A GET (SSE stream open) or DELETE (session end) carries no JSON-RPC body,
	// so it has no method to record.
	methodLabelUnknown = "unknown"

	// methodLabelOther marks a parsed method outside the known set. It keeps the
	// signal that an unregistered method was attempted. It drops the value.
	methodLabelOther = "other"
)

// knownMCPMethods is the bounded set of MCP method names recorded verbatim as a
// metric label value. A method outside this set collapses to methodLabelOther.
//
// The JSON-RPC method field comes from the raw request body before validation.
// An unauthenticated caller controls it. A label built from that field grows the
// metric registry without limit as new values arrive (CWE-770, CWE-400). The
// allowlist bounds the label cardinality regardless of input.
//
// The set covers the MCP specification method names, both requests and
// notifications. Keep it in sync with the MCP spec when methods are added.
var knownMCPMethods = func() map[string]struct{} {
	methods := []string{
		// Request and response methods defined as constants in toolhive-core.
		string(mcp.MethodInitialize),
		string(mcp.MethodPing),
		string(mcp.MethodResourcesList),
		string(mcp.MethodResourcesTemplatesList),
		string(mcp.MethodResourcesRead),
		string(mcp.MethodPromptsList),
		string(mcp.MethodPromptsGet),
		string(mcp.MethodToolsList),
		string(mcp.MethodToolsCall),
		string(mcp.MethodSetLogLevel),
		string(mcp.MethodElicitationCreate),
		string(mcp.MethodComplete),
		string(mcp.MethodListRoots),
		string(mcp.MethodNotificationInitialized),

		// Spec methods not yet defined as constants in toolhive-core. Keep as
		// literals until the upstream constants exist.
		"resources/subscribe",
		"resources/unsubscribe",
		"sampling/createMessage",
		"notifications/cancelled",
		"notifications/progress",
		"notifications/message",
		"notifications/resources/updated",
		"notifications/resources/list_changed",
		"notifications/tools/list_changed",
		"notifications/prompts/list_changed",
		"notifications/roots/list_changed",
	}
	set := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		set[method] = struct{}{}
	}
	return set
}()

// sanitizeMCPMethod maps a parsed MCP method to a bounded metric label value.
// It returns the method verbatim when the method is in the known set. It returns
// methodLabelUnknown for an empty or unparsed method. It returns methodLabelOther
// for any other value. This bounds metric label cardinality.
func sanitizeMCPMethod(method string) string {
	if method == "" || method == methodLabelUnknown {
		return methodLabelUnknown
	}
	if _, ok := knownMCPMethods[method]; ok {
		return method
	}
	return methodLabelOther
}

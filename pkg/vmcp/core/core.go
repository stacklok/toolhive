// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package core defines the Virtual MCP domain object: the identity-parameterized
// [VMCP] interface and the [Config] of collaborators that New assembles into a
// working core.
//
// The core is the domain boundary extracted from today's transport god-object
// (server.New): it aggregates backend capabilities, routes calls, and drives
// composite workflows behind a single, identity-explicit contract. It lives in
// its own package — rather than the root pkg/vmcp package — because Config
// references the aggregator, router, composer, health, and authz collaborators,
// all of which import pkg/vmcp; placing the core here lets it depend on both the
// root domain types and those collaborators without an import cycle.
//
// No mcp-go types cross this boundary (vmcp anti-pattern #5): the interface speaks
// only in root pkg/vmcp domain types ([vmcp.Tool], [vmcp.Resource], [vmcp.Prompt],
// and their result wrappers) plus an explicit [*auth.Identity].
package core

import (
	"context"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

// VMCP is the core Virtual MCP domain object.
//
// Contract:
//   - Identity is an explicit parameter on every method and is NEVER read from
//     context (anti-pattern #1). A nil identity is anonymous; bound-identity
//     mismatch is the caller's concern only at the session layer (in Serve), not
//     here — the core takes an already-authenticated *auth.Identity. The
//     nil-identity/anonymous semantics mirror session/types.ShouldAllowAnonymous;
//     the concrete behavior is reproduced by New.
//   - Implementations MUST be safe for concurrent use.
//   - Decorators may only SUBTRACT reachability: filter list output or refuse a
//     call before delegating to inner. They hold only an inner VMCP and have no
//     path to backends except through it, so they cannot widen access. This
//     mirrors the list-filter/call-deny pairing in pkg/authz/tool_filter.go.
//   - args/meta maps are treated as read-only; the core copies before mutating
//     (go-style: copy before mutating caller input).
//   - ReadResource results: Meta may be nil — the mcp-go resources/read handler
//     cannot forward _meta (see the NOTE on [vmcp.ResourceReadResult].Meta).
//     Do not rely on it.
//
// ctx is used only for cancellation, deadlines, and trace propagation — never to
// carry identity or other domain data.
type VMCP interface {
	// ListTools returns the tools advertised to identity. The returned
	// [vmcp.Tool] values carry their logical BackendID.
	ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error)

	// CallTool invokes the named tool on behalf of identity.
	//
	// args contains the tool input parameters. meta carries protocol-level
	// metadata (_meta) forwarded from the client; it is retained on this method
	// (rather than dropped) so the core can forward it through
	// [vmcp.BackendClient.CallTool] to the backend MCP server. Both maps are
	// treated as read-only.
	CallTool(ctx context.Context, identity *auth.Identity, name string,
		args map[string]any, meta map[string]any) (*vmcp.ToolCallResult, error)

	// ListResources returns the resources advertised to identity.
	ListResources(ctx context.Context, identity *auth.Identity) ([]vmcp.Resource, error)

	// ReadResource reads the resource at uri on behalf of identity. The
	// returned result's Meta may be nil (see the interface contract).
	ReadResource(ctx context.Context, identity *auth.Identity, uri string) (*vmcp.ResourceReadResult, error)

	// ListPrompts returns the prompts advertised to identity.
	ListPrompts(ctx context.Context, identity *auth.Identity) ([]vmcp.Prompt, error)

	// GetPrompt retrieves the named prompt on behalf of identity.
	//
	// args contains the prompt input parameters and is treated as read-only.
	// Unlike CallTool there is no meta parameter: prompts/get carries no client
	// _meta to forward.
	GetPrompt(ctx context.Context, identity *auth.Identity, name string,
		args map[string]any) (*vmcp.PromptGetResult, error)

	// LookupTool resolves an advertised tool name to its capability (incl.
	// BackendID) WITHOUT invoking it. Returns an error for an unknown or
	// unadvertised name — the validation seam for the call path. Lookups apply
	// the same admission filter as ListTools, so they never resolve a denied
	// capability.
	LookupTool(ctx context.Context, identity *auth.Identity, name string) (*vmcp.Tool, error)

	// LookupResource resolves an advertised resource URI to its capability
	// (incl. BackendID) WITHOUT reading it. Returns an error for an unknown or
	// unadvertised URI. Applies the same admission filter as ListResources.
	LookupResource(ctx context.Context, identity *auth.Identity, uri string) (*vmcp.Resource, error)

	// LookupPrompt resolves an advertised prompt name to its capability (incl.
	// BackendID) WITHOUT retrieving it. Returns an error for an unknown or
	// unadvertised name. Applies the same admission filter as ListPrompts.
	LookupPrompt(ctx context.Context, identity *auth.Identity, name string) (*vmcp.Prompt, error)

	// Close releases core-held resources (backend connections, etc.).
	// Implementations must be idempotent: calling Close multiple times returns nil.
	Close() error
}

// Config holds the collaborators New assembles into the core. The fields are
// declared here as the contract; New's body (which wires them into a concrete
// *coreVMCP) lands in a later change.
//
// Cross-cutting TelemetryProvider/AuditConfig are consumed by both New and Serve
// (not a clean partition). HealthStatusProvider is the read-only health view
// built at the composition root and injected here; a nil provider means no health
// filtering (all backends included), matching today's no-monitor behavior.
type Config struct {
	// Aggregator discovers and merges backend capabilities into the advertised set.
	Aggregator aggregator.Aggregator

	// Router resolves an advertised capability to its backend target.
	Router router.Router

	// BackendRegistry enumerates the configured backends.
	BackendRegistry vmcp.BackendRegistry

	// BackendClient performs MCP protocol calls against backend servers.
	BackendClient vmcp.BackendClient

	// WorkflowDefs holds the composite-tool workflow definitions, keyed by name.
	WorkflowDefs map[string]*composer.WorkflowDefinition

	// Authz feeds the admission seam New builds. A nil Authz means authorization
	// is unconfigured (allow-all), matching today's `AuthzMiddleware != nil` guard:
	// the composition root only populates this when Cedar policies exist (mirroring
	// newCedarAuthzMiddleware's `len(Policies) > 0` check).
	Authz *authz.Config

	// ServerName is the VirtualMCPServer name used as the Cedar resource entity
	// name in authorization policy evaluation — parity with the serverName threaded
	// into the HTTP authz middleware (factory/incoming.go:94). Consulted only when
	// Authz is set.
	ServerName string

	// PassThroughTools names the optimizer meta-tools (find_tool/call_tool) that are
	// exempt from admission filtering and denial, mirroring how they bypass the HTTP
	// authz response filter today (cli/serve.go:356-362). Nil when the optimizer is
	// disabled.
	PassThroughTools map[string]struct{}

	// TelemetryProvider is the cross-cutting telemetry provider (also consumed by Serve).
	TelemetryProvider *telemetry.Provider

	// AuditConfig is the cross-cutting audit configuration (also consumed by Serve).
	AuditConfig *audit.Config

	// HealthStatusProvider is the read-only backend health view built at the
	// composition root. Nil means no health filtering (all backends included).
	HealthStatusProvider health.StatusProvider

	// Elicitation sends MCP elicitation requests to the client and blocks for the
	// response. It is the domain-typed seam (vmcp anti-pattern #5: no mcp-go types)
	// consumed by the composer's elicitation handler during composite-tool
	// workflows. The composition root supplies the SDK-backed adapter; the core
	// only forwards it to the workflow engine. May be nil when no configured
	// workflow performs elicitation; New rejects a nil Elicitation when any
	// configured workflow contains an elicitation step.
	Elicitation vmcp.ElicitationRequester
}

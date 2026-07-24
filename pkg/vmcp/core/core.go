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
//     mirrors the list-filter/call-deny pairing in pkg/authz/tool_filter.go, and
//     applies to backends too: a decorator may drop entries from ListBackends
//     output or refuse a LookupBackend, but never surface a backend inner withheld.
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

	// ListResourceTemplates returns the resource templates advertised to identity.
	// Each template's URI template is treated as the resource id for the admission
	// filter (the same read-side decision ListResources applies), so a template the
	// identity may not read is withheld. Reading an expanded URI reuses ReadResource
	// (the router matches the URI against these templates); there is no dedicated
	// template read method.
	ListResourceTemplates(ctx context.Context, identity *auth.Identity) ([]vmcp.ResourceTemplate, error)

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

	// Complete resolves argument-completion candidates for a prompt argument or a
	// resource-template variable on behalf of identity (the completion/complete
	// method).
	//
	// ref selects the target: a CompletionRefTypePrompt ref resolves the backend via
	// the prompts routing table (by ref.Name); a CompletionRefTypeResource ref resolves
	// it via the resource-templates routing table (matching ref.URI against templates)
	// with an exact concrete-resource fallback. The referenced capability is
	// admission-checked (the same get/read decision GetPrompt/ReadResource enforce)
	// before the backend is queried.
	//
	// argName/argValue are the argument being completed; contextArgs carries
	// previously-resolved argument values for multi-argument completion (may be nil).
	//
	// An unroutable ref, or a backend that does not advertise completions, yields an
	// empty (non-nil) CompletionResult rather than an error — matching the MCP spec's
	// lenient completion semantics. Admission denial still returns an error wrapping
	// vmcp.ErrAuthorizationFailed. See ListTools for the nil/anonymous identity semantics.
	Complete(ctx context.Context, identity *auth.Identity, ref vmcp.CompletionRef,
		argName, argValue string, contextArgs map[string]string) (*vmcp.CompletionResult, error)

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

	// CheckToolCall runs the CALL-side admission decision for the named tool
	// WITHOUT invoking it: it returns nil when identity is allowed to call name
	// with args, and an error wrapping [vmcp.ErrAuthorizationFailed] on deny AND on
	// an authorizer error (fail closed). It shares the exact admission block CallTool
	// runs, so a pre-flight check and the call can never drift.
	//
	// Unlike LookupTool (list-side, no args), this is the call-side decision: it
	// evaluates argument-conditional policies with the real args. identity is never
	// logged. See ListTools for the nil/anonymous semantics.
	CheckToolCall(ctx context.Context, identity *auth.Identity, name string, args map[string]any) error

	// CheckResourceRead runs the read-side admission decision for uri WITHOUT
	// reading it. Same contract as CheckToolCall: nil when allowed, an error
	// wrapping [vmcp.ErrAuthorizationFailed] on deny or authorizer error (fail
	// closed). Shares ReadResource's admission block.
	CheckResourceRead(ctx context.Context, identity *auth.Identity, uri string) error

	// CheckPromptGet runs the get-side admission decision for the named prompt
	// WITHOUT retrieving it. Same contract as CheckToolCall: nil when allowed, an
	// error wrapping [vmcp.ErrAuthorizationFailed] on deny or authorizer error (fail
	// closed). Shares GetPrompt's admission block.
	CheckPromptGet(ctx context.Context, identity *auth.Identity, name string) error

	// ListBackends returns the backends visible to identity, scoped to the gateway's
	// group (groupRef always applies; the BackendRegistry is the group-scoped source).
	//
	// filterUnauthorized selects the view:
	//
	//   - true (user-discovery): returns only backends the identity is authorized to
	//     use — a backend appears iff the identity is admitted (the same seam
	//     ListTools/CallTool enforce) to AT LEAST ONE of that backend's discovered
	//     capabilities. There is no backend-level authorization entity, so this is a
	//     derived rule: aggregate the group's backends, run admission, and group the
	//     surviving capabilities by BackendID. A backend with zero discovered
	//     capabilities (tool-less) is absent for EVERY identity — this is NOT an
	//     authorization-denial signal; a backend that has capabilities but all are
	//     denied to this identity is also absent, for the unrelated reason of policy.
	//     Callers must not conflate "absent from this list" with "identity is denied
	//     this specific backend." A nil identity is anonymous (see the interface
	//     contract), yielding the anonymous-visible set.
	//
	//   - false (admin): returns ALL group-scoped backends with no per-identity
	//     authorization filtering. identity is unused and may be nil. Authorizing the
	//     caller for this view is the transport/API layer's responsibility (an
	//     admin-gated endpoint), not the core's.
	//
	// Health is a status, not a visibility filter: ListBackends does NOT apply the
	// health filter ListTools uses. A degraded/unhealthy backend still appears (admin
	// mode) — carrying its HealthStatus so a UI can badge it — and appears in the
	// authorized mode whenever it still has an admitted capability. Unreachable
	// backends contribute no capabilities to the authorized derivation, so they are
	// naturally absent there, but that is a consequence of live aggregation, not a
	// health filter. The returned slice is fresh and non-nil; callers may mutate it.
	ListBackends(ctx context.Context, identity *auth.Identity, filterUnauthorized bool) ([]vmcp.Backend, error)

	// LookupBackend resolves a single backend id in the AUTHORIZED view: it returns
	// the backend only when it is in ListBackends(ctx, identity, true), and
	// vmcp.ErrNotFound for an unknown, out-of-group, or unauthorized id. This mirrors
	// LookupTool/LookupResource/LookupPrompt — a lookup never resolves what the
	// corresponding authorized list would not show.
	LookupBackend(ctx context.Context, identity *auth.Identity, backendID string) (*vmcp.Backend, error)

	// BackendHealth returns the backend health reporter the core owns, or nil when health
	// monitoring is disabled. The core builds, starts, and (via Close) stops the monitor and
	// filters capabilities with it; the transport layer uses this only to report on or sync
	// backend health (e.g. the /health route and periodic status reporting).
	BackendHealth() health.Reporter

	// InvalidateCapabilityCache forces the next List/Lookup/Call on every identity
	// to re-sweep backend capabilities rather than serve a cached view. Serve's
	// Server calls it after a backend reports notifications/tools/list_changed
	// (#5748), before re-deriving the session's advertised tool set — otherwise
	// the resync would immediately re-read the now-stale cached aggregation. It
	// is a no-op (WARN-logged, not silent) when the configured Aggregator does
	// not memoize results — see aggregator.CacheInvalidator.
	InvalidateCapabilityCache()

	// Close releases core-held resources (backend connections, the health monitor, etc.).
	// Implementations must be idempotent: calling Close multiple times returns nil.
	Close() error
}

// Config holds the collaborators New assembles into the core. The fields are
// declared here as the contract; New's body (which wires them into a concrete
// *coreVMCP) lands in a later change.
//
// Cross-cutting TelemetryProvider/AuditConfig are consumed by both New and Serve
// (not a clean partition). HealthMonitorConfig is the backend health monitoring
// configuration; the core builds, starts, and stops the monitor from it (a nil config
// disables monitoring, including all backends — matching today's no-monitor behavior).
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
	// newCedarAuthzMiddleware's `len(Policies) > 0` check). When Authz is non-nil,
	// ServerName is REQUIRED (New fails fast with vmcp.ErrInvalidConfig otherwise).
	Authz *authz.Config

	// ServerName is the VirtualMCPServer name used as the Cedar resource entity name
	// in authorization policy evaluation — parity with the serverName threaded into
	// the HTTP authz middleware. It is REQUIRED when Authz is non-nil (New returns
	// vmcp.ErrInvalidConfig on an empty value, since Cedar resource-scoped policies
	// depend on it) and ignored when Authz is nil.
	ServerName string

	// TelemetryProvider is the cross-cutting telemetry provider (also consumed by Serve).
	TelemetryProvider *telemetry.Provider

	// AuditConfig is the cross-cutting audit configuration (also consumed by Serve).
	AuditConfig *audit.Config

	// HealthMonitorConfig configures backend health monitoring. The core builds, starts, and
	// (via Close) stops the monitor from it, filters capabilities with it, and exposes it via
	// BackendHealth. Nil disables monitoring (no health filtering; all backends included).
	HealthMonitorConfig *health.MonitorConfig

	// Elicitation sends MCP elicitation requests to the client and blocks for the
	// response. It is the domain-typed seam (vmcp anti-pattern #5: no mcp-go types)
	// consumed by the composer's elicitation handler during composite-tool
	// workflows. The composition root supplies the SDK-backed adapter; the core
	// only forwards it to the workflow engine. May be nil when no configured
	// workflow performs elicitation; New rejects a nil Elicitation when any
	// configured workflow contains an elicitation step.
	Elicitation vmcp.ElicitationRequester
}

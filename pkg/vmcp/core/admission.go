// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Admission decides whether an identity may see or use a capability. It wraps the
// existing [authorizers.Authorizer] (Cedar in the vMCP path); it does NOT define a
// new policy model.
//
// The seam re-platforms the authorization decision that runs as HTTP middleware
// today (AuthzMiddleware + AnnotationEnrichmentMiddleware) into the core, so that
// List* (filter) and Call/Read/Get (deny) enforce the SAME decision from one
// source — closing the "list says yes / call says no" gap. Identity is an explicit
// parameter (never read from ctx, vmcp anti-pattern #1); the adapter re-injects it
// into the ctx the wrapped authorizer reads. A seam can only SUBTRACT reachability
// (filter list output / refuse a call), never widen it ([VMCP] contract).
//
// The seam is optimizer-agnostic: it authorizes every capability uniformly by name
// and has no special handling for the optimizer's meta-tools (find_tool/call_tool).
// The optimizer-admission integration the HTTP path performs today — call_tool
// inner-target authorization, find_tool response filtering, and inner-target
// annotation sourcing — is deliberately NOT in this seam; it is deferred to its own
// focused PR. Until then the optimizer keeps the HTTP middleware, and #5441's
// composition root fails fast (vmcp.ErrInvalidConfig) when Authz is set together
// with the optimizer, so the combination can never silently route through this seam.
type Admission interface {
	// FilterTools returns the subset of tools the identity may call. Mirrors
	// pkg/authz filterToolsByPolicy: a per-tool AuthorizeWithJWTClaims(call) using
	// the tool's annotations; a per-tool authorizer error skips that tool
	// (log-and-continue), it is not a hard failure.
	FilterTools(ctx context.Context, identity *auth.Identity, tools []vmcp.Tool) ([]vmcp.Tool, error)
	// AllowToolCall mirrors pkg/authz authorizeToolCall (MCPFeatureTool/Call),
	// sourcing the tool's annotations for when-clause evaluation.
	AllowToolCall(ctx context.Context, identity *auth.Identity, tool *vmcp.Tool, args map[string]any) (bool, error)
	// FilterResources / AllowResourceRead use MCPFeatureResource/Read.
	FilterResources(ctx context.Context, identity *auth.Identity, resources []vmcp.Resource) ([]vmcp.Resource, error)
	AllowResourceRead(ctx context.Context, identity *auth.Identity, resource *vmcp.Resource) (bool, error)
	// FilterPrompts / AllowPromptGet use MCPFeaturePrompt/Get.
	FilterPrompts(ctx context.Context, identity *auth.Identity, prompts []vmcp.Prompt) ([]vmcp.Prompt, error)
	AllowPromptGet(ctx context.Context, identity *auth.Identity, prompt *vmcp.Prompt) (bool, error)
}

// newAdmission builds the admission seam New wires into the core.
//
// A nil authzCfg means authorization is unconfigured and the seam is allow-all,
// preserving today's `AuthzMiddleware != nil` guard: the composition root only
// populates Authz when Cedar policies exist (mirroring newCedarAuthzMiddleware's
// `len(Policies) > 0` check), so "no policies" reaches the core as a nil Authz.
//
// A non-nil authzCfg with zero policies is NOT treated as allow-all here: it falls
// through to CreateAuthorizer, which returns ErrNoPolicies, so the core fails
// CLOSED. This is a deliberate divergence from the live HTTP path, which allows-all
// for the same input. Fail-closed is the safer default; #5441 reconciles the two
// when it collapses the construction paths into one shared constructor.
//
// When authzCfg is set, the authorizer is built via the same registry path the
// HTTP middleware uses (authorizers.GetFactory + CreateAuthorizer, the construction
// inside newCedarAuthzMiddleware). The Cedar factory is registered by pkg/authz's
// blank import, which the core package already pulls in.
//
// authzCfg is an already-built, authorizer-agnostic config: the cedar-specific
// normalization newCedarAuthzMiddleware performs at construction time (e.g.
// defaulting an empty EntitiesJSON to "[]") belongs to whoever builds authzCfg, not
// to this generic factory path, which consumes RawConfig as-is. newCedarAuthzMiddleware
// is the sibling construction path; the two must stay in lockstep (notably the
// empty-serverName check below) until #5441 collapses them.
func newAdmission(authzCfg *authorizers.Config, serverName string) (Admission, error) {
	if authzCfg == nil {
		return allowAllAdmission{}, nil
	}

	// Fail loudly on an empty server name, mirroring newCedarAuthzMiddleware's check.
	// The Cedar entity factory attaches the MCP::"<serverName>" parent UID to resource
	// entities only when serverName is non-empty, so an empty name silently changes
	// resource-scoped policy semantics rather than erroring (go-style: fail loudly on
	// invalid input).
	if serverName == "" {
		return nil, fmt.Errorf(
			"%w: ServerName must not be empty when Authz is set (Cedar resource-scoped policies require it)",
			vmcp.ErrInvalidConfig)
	}

	factory := authorizers.GetFactory(string(authzCfg.Type))
	if factory == nil {
		return nil, fmt.Errorf("%w: unsupported authz type %q", vmcp.ErrInvalidConfig, authzCfg.Type)
	}
	authorizer, err := factory.CreateAuthorizer(authzCfg.RawConfig(), serverName)
	if err != nil {
		// Classify with ErrInvalidConfig so callers (errors.Is) treat a bad policy /
		// malformed RawConfig the same as the unsupported-type and empty-serverName
		// failures above — all are invalid-config conditions.
		return nil, fmt.Errorf("%w: failed to build admission authorizer: %w", vmcp.ErrInvalidConfig, err)
	}
	return newCedarAdmission(authorizer), nil
}

// cedarAdmission enforces the wrapped [authorizers.Authorizer]'s decision. It is
// named for the Cedar authorizer it backs in the vMCP path, but wraps the
// single-method Authorizer interface generically. It authorizes every capability
// uniformly — no optimizer meta-tool carve-out (see the [Admission] doc).
type cedarAdmission struct {
	authorizer authorizers.Authorizer
	// logger receives the per-item "skipping" warnings. It defaults to
	// slog.Default(); tests inject a buffer-backed logger (via withLogger) so they
	// can assert on output WITHOUT swapping the process-global slog default — that
	// global swap races (-race) against parallel sibling tests that also log.
	logger *slog.Logger
}

// cedarOption configures a cedarAdmission at construction.
type cedarOption func(*cedarAdmission)

// withLogger overrides the logger the seam emits skip warnings to. Used by tests
// to capture log output without mutating the global slog default.
func withLogger(logger *slog.Logger) cedarOption {
	return func(c *cedarAdmission) { c.logger = logger }
}

// newCedarAdmission wraps an already-built authorizer. Separated from newAdmission
// so tests can inject a stub authorizer (or a real cedar.NewCedarAuthorizer)
// without round-tripping through the config/factory.
func newCedarAdmission(a authorizers.Authorizer, opts ...cedarOption) *cedarAdmission {
	adm := &cedarAdmission{authorizer: a, logger: slog.Default()}
	for _, opt := range opts {
		opt(adm)
	}
	return adm
}

// FilterTools mirrors pkg/authz filterToolsByPolicy: each tool is authorized for
// call with its annotations injected, and a per-tool authorizer error skips that
// tool (log-and-continue).
func (a *cedarAdmission) FilterTools(
	ctx context.Context, identity *auth.Identity, tools []vmcp.Tool,
) ([]vmcp.Tool, error) {
	ctx = auth.WithIdentity(ctx, identity)
	// A filter returns a subset, so build a fresh non-nil slice — mirroring
	// pkg/authz filterToolsByPolicy (its configured-authorizer path is also
	// non-nil; its nil-authorizer no-op, like the allow-all seam here, returns the
	// input as-is). nil-vs-[] wire normalization is the Serve layer's concern.
	filtered := make([]vmcp.Tool, 0, len(tools))
	for i := range tools {
		tool := &tools[i]
		toolCtx := ctx
		if ann := convertAnnotations(tool.Annotations); ann != nil {
			toolCtx = authorizers.WithToolAnnotations(toolCtx, ann)
		}
		allowed, err := a.authorizer.AuthorizeWithJWTClaims(
			toolCtx, authorizers.MCPFeatureTool, authorizers.MCPOperationCall, tool.Name, nil)
		if err != nil {
			a.logger.Warn("admission: tool authorization check failed, skipping", "tool", tool.Name, "error", err)
			continue
		}
		if allowed {
			filtered = append(filtered, *tool)
		}
	}
	return filtered, nil
}

// AllowToolCall mirrors pkg/authz authorizeToolCall (MCPFeatureTool/Call), sourcing
// the tool's annotations for when-clause evaluation. It authorizes the tool named in
// `tool.Name` — there is no optimizer meta-tool special-casing here (see the
// [Admission] doc): the optimizer's call_tool inner-target authorization is deferred
// to a dedicated optimizer-admission PR, and #5441 fails fast on (Authz + optimizer)
// so that combination never reaches this seam in the meantime.
func (a *cedarAdmission) AllowToolCall(
	ctx context.Context, identity *auth.Identity, tool *vmcp.Tool, args map[string]any,
) (bool, error) {
	ctx = auth.WithIdentity(ctx, identity)
	if ann := convertAnnotations(tool.Annotations); ann != nil {
		ctx = authorizers.WithToolAnnotations(ctx, ann)
	}
	return a.authorizer.AuthorizeWithJWTClaims(
		ctx, authorizers.MCPFeatureTool, authorizers.MCPOperationCall, tool.Name, args)
}

// FilterResources mirrors the response filter's filterResourcesResponse
// (response_filter.go:407): per-resource read authorization, skipping on error.
func (a *cedarAdmission) FilterResources(
	ctx context.Context, identity *auth.Identity, resources []vmcp.Resource,
) ([]vmcp.Resource, error) {
	ctx = auth.WithIdentity(ctx, identity)
	filtered := make([]vmcp.Resource, 0, len(resources))
	for i := range resources {
		allowed, err := a.authorizer.AuthorizeWithJWTClaims(
			ctx, authorizers.MCPFeatureResource, authorizers.MCPOperationRead, resources[i].URI, nil)
		if err != nil {
			a.logger.Warn("admission: resource authorization check failed, skipping",
				"resource", resources[i].URI, "error", err)
			continue
		}
		if allowed {
			filtered = append(filtered, resources[i])
		}
	}
	return filtered, nil
}

// AllowResourceRead authorizes a single resource read (MCPFeatureResource/Read).
func (a *cedarAdmission) AllowResourceRead(
	ctx context.Context, identity *auth.Identity, resource *vmcp.Resource,
) (bool, error) {
	ctx = auth.WithIdentity(ctx, identity)
	return a.authorizer.AuthorizeWithJWTClaims(
		ctx, authorizers.MCPFeatureResource, authorizers.MCPOperationRead, resource.URI, nil)
}

// FilterPrompts mirrors the response filter's filterPromptsResponse
// (response_filter.go:346): per-prompt get authorization, skipping on error.
func (a *cedarAdmission) FilterPrompts(
	ctx context.Context, identity *auth.Identity, prompts []vmcp.Prompt,
) ([]vmcp.Prompt, error) {
	ctx = auth.WithIdentity(ctx, identity)
	filtered := make([]vmcp.Prompt, 0, len(prompts))
	for i := range prompts {
		allowed, err := a.authorizer.AuthorizeWithJWTClaims(
			ctx, authorizers.MCPFeaturePrompt, authorizers.MCPOperationGet, prompts[i].Name, nil)
		if err != nil {
			a.logger.Warn("admission: prompt authorization check failed, skipping",
				"prompt", prompts[i].Name, "error", err)
			continue
		}
		if allowed {
			filtered = append(filtered, prompts[i])
		}
	}
	return filtered, nil
}

// AllowPromptGet authorizes a single prompt get (MCPFeaturePrompt/Get).
func (a *cedarAdmission) AllowPromptGet(
	ctx context.Context, identity *auth.Identity, prompt *vmcp.Prompt,
) (bool, error) {
	ctx = auth.WithIdentity(ctx, identity)
	return a.authorizer.AuthorizeWithJWTClaims(
		ctx, authorizers.MCPFeaturePrompt, authorizers.MCPOperationGet, prompt.Name, nil)
}

// allowAllAdmission is the no-op seam used when authorization is unconfigured. It
// preserves today's behavior when AuthzMiddleware is nil: Filter* return their
// input unchanged and Allow* return true (the nil-authorizer no-ops at
// tool_filter.go:20,67).
type allowAllAdmission struct{}

func (allowAllAdmission) FilterTools(
	_ context.Context, _ *auth.Identity, tools []vmcp.Tool,
) ([]vmcp.Tool, error) {
	return tools, nil
}

func (allowAllAdmission) AllowToolCall(
	_ context.Context, _ *auth.Identity, _ *vmcp.Tool, _ map[string]any,
) (bool, error) {
	return true, nil
}

func (allowAllAdmission) FilterResources(
	_ context.Context, _ *auth.Identity, resources []vmcp.Resource,
) ([]vmcp.Resource, error) {
	return resources, nil
}

func (allowAllAdmission) AllowResourceRead(_ context.Context, _ *auth.Identity, _ *vmcp.Resource) (bool, error) {
	return true, nil
}

func (allowAllAdmission) FilterPrompts(
	_ context.Context, _ *auth.Identity, prompts []vmcp.Prompt,
) ([]vmcp.Prompt, error) {
	return prompts, nil
}

func (allowAllAdmission) AllowPromptGet(_ context.Context, _ *auth.Identity, _ *vmcp.Prompt) (bool, error) {
	return true, nil
}

// convertAnnotations maps vmcp.ToolAnnotations to authorizers.ToolAnnotations,
// returning nil when no hint field is set so the adapter only writes annotation
// ctx when a hint exists (matching the existing hasAnyHint gate). Only
// authorization-relevant hints are mapped; informational fields like Title are not
// used in policy evaluation.
//
// This duplicates server.convertAnnotations (annotation_enrichment.go:92) rather
// than reusing it: that copy lives in package server, which imports this core
// package, so importing it here would create a cycle (server -> core). Like the
// filterHealthyBackends C2 duplication in this package, it is intentional and
// temporary — #5441 retires the server-side middleware (and its copy) on the domain
// path. Keep the two in sync until then.
func convertAnnotations(ann *vmcp.ToolAnnotations) *authorizers.ToolAnnotations {
	if ann == nil {
		return nil
	}
	if ann.ReadOnlyHint == nil && ann.DestructiveHint == nil &&
		ann.IdempotentHint == nil && ann.OpenWorldHint == nil {
		return nil
	}
	return &authorizers.ToolAnnotations{
		ReadOnlyHint:    ann.ReadOnlyHint,
		DestructiveHint: ann.DestructiveHint,
		IdempotentHint:  ann.IdempotentHint,
		OpenWorldHint:   ann.OpenWorldHint,
	}
}

// findAdvertisedTool returns a pointer to the advertised tool with the given name,
// or nil. CallTool uses it to source the tool's annotations for the admission
// decision — mirroring the annotation cache the HTTP middleware populates from
// tools/list. A name absent from the advertised set carries no annotations
// (nil), and routing remains the authority on whether the call resolves.
func findAdvertisedTool(tools []vmcp.Tool, name string) *vmcp.Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

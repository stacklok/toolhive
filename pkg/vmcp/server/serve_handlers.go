// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// This file holds the Serve-path (s.core != nil) capability wiring. On the Serve
// path the core VMCP is the single authoritative aggregation: session
// registration sources the advertised tool/resource set from core.ListTools /
// core.ListResources (admission-filtered, composites included) and installs SDK
// handlers that route invocations through core.CallTool / core.ReadResource with
// an explicit *auth.Identity. The session factory does NOT aggregate on this path,
// so there is no second aggregation and no drift between the advertised set and
// the call path. Identity binding is still enforced by the session layer via
// enforceSessionBinding before each call reaches the core.
//
// The legacy server.New path (s.core == nil) is untouched: it sources capabilities
// from the session factory's aggregation via Manager.GetAdaptedTools/Resources and
// routes through the session's own MultiSession.

// injectCoreSessionCapabilities sources the session's advertised capability set
// from the core and installs it on the SDK session. It is invoked from
// handleSessionRegistrationImpl on the Serve path, after the bound session record
// has been created. The core.ListTools / core.ListResources calls here are the
// single CORE aggregation per session.
//
// "Single core aggregation" — not "the only backend work per session" — because the
// preceding CreateSession opens the session's backend connections via the factory.
// To honor AC2 (no double-aggregation, no drift), the composition root MUST configure
// the Serve-path session factory WITHOUT its own aggregator (see the contract on
// ServerConfig.SessionManagerConfig); otherwise the factory would aggregate a second,
// divergent set whose routing table this path discards.
//
// Prompts are injected here too, alongside tools and resources: the SDK's
// SessionWithPrompts interface (toolhive-core mcpcompat) supports per-session prompt
// injection, so core.ListPrompts sources the advertised set and corePromptHandler
// routes prompts/get through core.GetPrompt (an exact mirror of the resource path).
func (s *Server) injectCoreSessionCapabilities(ctx context.Context, session server.ClientSession) error {
	sessionID := session.SessionID()

	// Identity is read from the SDK hook context here, at the transport boundary,
	// and passed explicitly to the core — the core never reads it from context.
	identity, _ := auth.IdentityFromContext(ctx)

	// serveSessionTools returns the core's advertised tools, or — when the optimizer
	// is enabled on this path — the find_tool/call_tool meta-tools built over them.
	tools, err := s.serveSessionTools(ctx, sessionID, identity)
	if err != nil {
		slog.Error("failed to list core tools for session", "session_id", sessionID, "error", err)
		return err
	}
	resources, err := s.coreSessionResources(ctx, sessionID, identity)
	if err != nil {
		slog.Error("failed to list core resources for session", "session_id", sessionID, "error", err)
		return err
	}
	resourceTemplates, err := s.coreSessionResourceTemplates(ctx, sessionID, identity)
	if err != nil {
		slog.Error("failed to list core resource templates for session", "session_id", sessionID, "error", err)
		return err
	}
	prompts, err := s.coreSessionPrompts(ctx, sessionID, identity)
	if err != nil {
		slog.Error("failed to list core prompts for session", "session_id", sessionID, "error", err)
		return err
	}

	if len(resources) > 0 {
		if err := setSessionResourcesDirect(session, resources); err != nil {
			slog.Error("failed to add session resources", "session_id", sessionID, "error", err)
			return err
		}
	}
	if len(resourceTemplates) > 0 {
		if err := setSessionResourceTemplatesDirect(session, resourceTemplates); err != nil {
			slog.Error("failed to add session resource templates", "session_id", sessionID, "error", err)
			return err
		}
	}
	if len(prompts) > 0 {
		if err := setSessionPromptsDirect(session, prompts); err != nil {
			slog.Error("failed to add session prompts", "session_id", sessionID, "error", err)
			return err
		}
	}
	if len(tools) > 0 {
		if err := setSessionToolsDirect(session, tools); err != nil {
			slog.Error("failed to add session tools", "session_id", sessionID, "error", err)
			return err
		}
	}

	slog.Info("session capabilities injected from core",
		"session_id", sessionID,
		"tool_count", len(tools),
		"resource_count", len(resources),
		"resource_template_count", len(resourceTemplates),
		"prompt_count", len(prompts))
	return nil
}

// coreSessionTools queries the core for the tools advertised to identity and
// adapts them to SDK ServerTools whose handlers route through core.CallTool. The
// core owns conflict resolution and backend-name translation, so the SDK tool name
// is forwarded as-is to core.CallTool.
//
// The backend display name is resolved here (once per session, from the same
// ListTools aggregation) and captured in the handler closure, so the handler can
// label the audit event without a per-request lookup (see coreToolHandler).
func (s *Server) coreSessionTools(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerTool, error) {
	domainTools, err := s.core.ListTools(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("core ListTools: %w", err)
	}

	sdkTools := make([]server.ServerTool, 0, len(domainTools))
	for _, domainTool := range domainTools {
		schemaJSON, err := json.Marshal(domainTool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema for tool %s: %w", domainTool.Name, err)
		}

		tool := mcp.Tool{
			Name:           domainTool.Name,
			Description:    domainTool.Description,
			RawInputSchema: schemaJSON,
			Annotations:    conversion.ToMCPToolAnnotations(domainTool.Annotations),
		}
		// Unlike the required InputSchema (a marshal failure aborts registration above),
		// the optional OutputSchema is best-effort: on failure the tool is still advertised
		// without it. Mirrors the legacy GetAdaptedTools adapter.
		if domainTool.OutputSchema != nil {
			if outputSchemaJSON, marshalErr := json.Marshal(domainTool.OutputSchema); marshalErr != nil {
				slog.Warn("failed to marshal tool output schema", "tool", domainTool.Name, "error", marshalErr)
			} else {
				tool.RawOutputSchema = outputSchemaJSON
			}
		}

		sdkTools = append(sdkTools, server.ServerTool{
			Tool:    tool,
			Handler: s.coreToolHandler(sessionID, domainTool.Name, s.backendDisplayName(ctx, domainTool.BackendID)),
		})
	}
	return sdkTools, nil
}

// coreSessionResources queries the core for the resources advertised to identity
// and adapts them to SDK ServerResources whose handlers route through
// core.ReadResource. The backend display name is resolved here and captured in the
// handler closure for audit labelling (see coreResourceHandler).
func (s *Server) coreSessionResources(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerResource, error) {
	domainResources, err := s.core.ListResources(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("core ListResources: %w", err)
	}

	sdkResources := make([]server.ServerResource, 0, len(domainResources))
	for _, domainResource := range domainResources {
		sdkResources = append(sdkResources, server.ServerResource{
			Resource: mcp.Resource{
				Name:        domainResource.Name,
				URI:         domainResource.URI,
				Description: domainResource.Description,
				MIMEType:    domainResource.MimeType,
			},
			Handler: s.coreResourceHandler(sessionID, domainResource.URI, s.backendDisplayName(ctx, domainResource.BackendID)),
		})
	}
	return sdkResources, nil
}

// coreSessionResourceTemplates queries the core for the resource templates
// advertised to identity and adapts them to SDK ServerResourceTemplates whose
// handlers route through core.ReadResource. Mirrors coreSessionResources.
//
// The SDK serves resources/read for a URI matching a registered template through
// the template's handler, so the handler receives the concrete (expanded) URI on
// the request. It routes that URI through core.ReadResource, which resolves it via
// the router's template-match fallback (session_router.go) — there is no dedicated
// template read method. The backend display name is resolved here per template for
// audit labelling (see coreResourceTemplateHandler).
func (s *Server) coreSessionResourceTemplates(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerResourceTemplate, error) {
	domainTemplates, err := s.core.ListResourceTemplates(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("core ListResourceTemplates: %w", err)
	}

	sdkTemplates := make([]server.ServerResourceTemplate, 0, len(domainTemplates))
	for _, domainTemplate := range domainTemplates {
		sdkTemplates = append(sdkTemplates, server.ServerResourceTemplate{
			Template: mcp.ResourceTemplate{
				Name:        domainTemplate.Name,
				URITemplate: domainTemplate.URITemplate,
				Description: domainTemplate.Description,
				MIMEType:    domainTemplate.MimeType,
			},
			Handler: s.coreResourceTemplateHandler(
				sessionID, s.backendDisplayName(ctx, domainTemplate.BackendID)),
		})
	}
	return sdkTemplates, nil
}

// coreSessionPrompts queries the core for the prompts advertised to identity and
// adapts them to SDK ServerPrompts whose handlers route through core.GetPrompt. The
// backend display name is resolved here and captured in the handler closure for audit
// labelling (see corePromptHandler). Mirrors coreSessionResources.
func (s *Server) coreSessionPrompts(
	ctx context.Context, sessionID string, identity *auth.Identity,
) ([]server.ServerPrompt, error) {
	domainPrompts, err := s.core.ListPrompts(ctx, identity)
	if err != nil {
		return nil, fmt.Errorf("core ListPrompts: %w", err)
	}

	sdkPrompts := make([]server.ServerPrompt, 0, len(domainPrompts))
	for _, domainPrompt := range domainPrompts {
		arguments := make([]mcp.PromptArgument, 0, len(domainPrompt.Arguments))
		for _, arg := range domainPrompt.Arguments {
			arguments = append(arguments, mcp.PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			})
		}
		sdkPrompts = append(sdkPrompts, server.ServerPrompt{
			Prompt: mcp.Prompt{
				Name:        domainPrompt.Name,
				Description: domainPrompt.Description,
				Arguments:   arguments,
			},
			Handler: s.corePromptHandler(sessionID, domainPrompt.Name, s.backendDisplayName(ctx, domainPrompt.BackendID)),
		})
	}
	return sdkPrompts, nil
}

// coreToolHandler builds the SDK handler for a Serve-path tool. It labels the audit
// event with backendName (the serving backend, pre-resolved at registration),
// enforces the session's identity binding, then delegates to core.CallTool with the
// caller's explicit identity. Admission (authorization) is the core's responsibility.
func (s *Server) coreToolHandler(sessionID, toolName, backendName string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Label the audit event with the backend serving this tool. The audit
		// middleware created the BackendInfo upstream and reads it after this handler
		// returns (auditor.Middleware), so writing the pre-resolved name here — where
		// the call is actually handled — is what lets the Serve path drop the separate
		// backend-enrichment middleware and its per-request lookup (#5512 review).
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = backendName
		}

		// Shape-check on the SDK decode first: a non-object payload is rejected before the
		// core is reached. This validates request shape for the parse we forward too — both
		// decode the same request bytes, so a valid transport parse implies a valid SDK
		// shape (the substitution below only changes which equal-shaped map is used).
		args, ok := req.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, req.Params.Arguments)
			slog.Warn("invalid arguments for tool", "tool", toolName, "error", wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		// Prefer the transport-parsed argument map so the pre-dispatch authz gate's
		// decision, this handler's enforced decision, and the forwarded backend call
		// all derive from one decode (#5845). A nil result means no matching parse is
		// available (batch / embedders bypassing transport middleware / method or tool
		// mismatch); those paths keep the SDK decode above and still make a single
		// decision on a single map, so gate and dispatch cannot diverge.
		if pa := gateParsedArgs(ctx, toolName); pa != nil {
			args = pa
		}

		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Unauthorized: %v", err)), nil
		}

		result, err := s.core.CallTool(ctx, caller, toolName, args, conversion.FromMCPMeta(req.Params.Meta))
		if err != nil {
			return conversion.ErrorToToolResult(err), nil
		}

		return &mcp.CallToolResult{
			Result:            mcp.Result{Meta: conversion.ToMCPMeta(result.Meta)},
			Content:           conversion.ToMCPContents(result.Content),
			StructuredContent: result.StructuredContent,
			IsError:           result.IsError,
		}, nil
	}
}

// gateParsedArgs returns the argument map the pre-dispatch authz gate decided on
// (pkg/mcp's transport parse), so the gated decision, this handler's enforced
// decision, and the forwarded backend call all share one decode (#5845). It returns
// parsed.Arguments only when the context carries a ParsedMCPRequest for the SAME
// tools/call on the SAME tool with non-nil arguments.
//
// A nil result tells the caller to keep the SDK decode: batch requests, embedders
// that bypass the transport middleware, or a method/tool mismatch leave no matching
// parse. Those paths make a single decision on a single map (the SDK decode), so
// there is still no allow-then-deny divergence between gate and dispatch.
func gateParsedArgs(ctx context.Context, toolName string) map[string]any {
	parsed := mcpparser.GetParsedMCPRequest(ctx)
	if parsed == nil || parsed.Method != "tools/call" || parsed.ResourceID != toolName || parsed.Arguments == nil {
		return nil
	}
	return parsed.Arguments
}

// coreResourceHandler builds the SDK handler for a Serve-path resource. It mirrors
// coreToolHandler: audit label, binding check, then core.ReadResource with explicit identity.
func (s *Server) coreResourceHandler(
	sessionID, uri, backendName string,
) func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = backendName
		}

		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, uri, err)
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		result, err := s.core.ReadResource(ctx, caller, uri)
		if err != nil {
			if errors.Is(err, vmcp.ErrAuthorizationFailed) {
				return nil, errors.New(vmcp.DenyMessageResourceRead)
			}
			return nil, err
		}
		return conversion.ToMCPResourceContents(result.Contents), nil
	}
}

// coreResourceTemplateHandler builds the SDK handler for a Serve-path resource
// template. It mirrors coreResourceHandler but reads the concrete URI from the
// request (req.Params.URI) rather than a fixed URI captured at registration,
// because one template serves a whole family of URIs. It routes that URI through
// core.ReadResource, which resolves it via the router's template-match fallback.
func (s *Server) coreResourceTemplateHandler(
	sessionID, backendName string,
) func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = backendName
		}

		uri := req.Params.URI

		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, uri, err)
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		result, err := s.core.ReadResource(ctx, caller, uri)
		if err != nil {
			if errors.Is(err, vmcp.ErrAuthorizationFailed) {
				return nil, errors.New(vmcp.DenyMessageResourceRead)
			}
			return nil, err
		}
		return conversion.ToMCPResourceContents(result.Contents), nil
	}
}

// corePromptHandler builds the SDK handler for a Serve-path prompt. It mirrors
// coreResourceHandler: audit label, binding check, then core.GetPrompt with explicit
// identity. The request's string-typed prompt arguments are widened to map[string]any
// for the domain call, and the domain result is converted to *mcp.GetPromptResult.
func (s *Server) corePromptHandler(
	sessionID, promptName, backendName string,
) server.PromptHandlerFunc {
	return func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		if bi, ok := audit.BackendInfoFromContext(ctx); ok && bi != nil {
			bi.BackendName = backendName
		}

		caller, _ := auth.IdentityFromContext(ctx)
		if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
			s.terminateOnBindingFailure(sessionID, promptName, err)
			return nil, fmt.Errorf("unauthorized: %w", err)
		}

		args := make(map[string]any, len(req.Params.Arguments))
		for k, v := range req.Params.Arguments {
			args[k] = v
		}

		result, err := s.core.GetPrompt(ctx, caller, promptName, args)
		if err != nil {
			if errors.Is(err, vmcp.ErrAuthorizationFailed) {
				return nil, errors.New(vmcp.DenyMessagePromptGet)
			}
			return nil, err
		}
		return &mcp.GetPromptResult{
			Result:      mcp.Result{Meta: conversion.ToMCPMeta(result.Meta)},
			Description: result.Description,
			Messages:    conversion.ToMCPPromptMessages(result.Messages),
		}, nil
	}
}

// coreCompletionHandler is the SDK completion/complete handler on the Serve path.
// Unlike the per-session tool/resource/prompt handlers (whose closures capture a
// sessionID at registration), completion is a single global handler installed on the
// mcp-go server via server.WithCompletionHandler, so it resolves the session ID from
// the SDK request context. It mirrors the other core handlers: audit label,
// enforceSessionBinding, then routes through core.Complete with the caller's explicit
// identity resolved at the transport boundary.
//
// It converts the mcp-go CompleteRequest ref (a PromptReference or ResourceReference,
// typed as any) into the domain vmcp.CompletionRef, and the domain
// *vmcp.CompletionResult back into *mcp.CompleteResult. An admission denial surfaces
// as the standard deny message for the referenced capability kind.
func (s *Server) coreCompletionHandler(
	ctx context.Context, req mcp.CompleteRequest,
) (*mcp.CompleteResult, error) {
	ref, err := completionRefFromMCP(req.Params.Ref)
	if err != nil {
		return nil, err
	}

	sessionID := ""
	if sess := server.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
	}

	caller, _ := auth.IdentityFromContext(ctx)
	if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
		s.terminateOnBindingFailure(sessionID, "completion/complete", err)
		return nil, fmt.Errorf("unauthorized: %w", err)
	}

	var contextArgs map[string]string
	if req.Params.Context != nil {
		contextArgs = req.Params.Context.Arguments
	}

	result, err := s.core.Complete(
		ctx, caller, ref, req.Params.Argument.Name, req.Params.Argument.Value, contextArgs,
	)
	if err != nil {
		if errors.Is(err, vmcp.ErrAuthorizationFailed) {
			return nil, errors.New(completionDenyMessage(ref.Type))
		}
		return nil, err
	}

	return &mcp.CompleteResult{
		Completion: mcp.CompletionResultDetails{
			Values:  result.Values,
			Total:   result.Total,
			HasMore: result.HasMore,
		},
	}, nil
}

// coreSubscribeHandler answers resources/subscribe and resources/unsubscribe at
// ack level. Like coreCompletionHandler it is a single global handler installed on
// the mcp-go server (not a per-session closure), so it resolves the session ID from
// the SDK request context. It mirrors the other core handlers: resolve the caller's
// explicit identity at the transport boundary, enforce the session binding, then
// validate that uri is an advertised resource the caller may read (core.LookupResource
// applies the same admission decision ListResources/ReadResource enforce). An unknown
// or admission-denied URI is rejected; on success it returns nil so go-sdk records the
// subscription and answers the client with a success ack.
//
// Scope limitation (intentional): vMCP accepts the subscription but does NOT yet
// forward backend resources/updated notifications to the client — doing so needs
// persistent per-session backend connections, which is out of scope here. capability
// is the method name used for binding-failure diagnostics.
func (s *Server) coreSubscribeHandler(ctx context.Context, capability, uri string) error {
	sessionID := ""
	if sess := server.ClientSessionFromContext(ctx); sess != nil {
		sessionID = sess.SessionID()
	}

	caller, _ := auth.IdentityFromContext(ctx)
	if err := s.enforceSessionBinding(ctx, sessionID, caller); err != nil {
		s.terminateOnBindingFailure(sessionID, capability, err)
		return fmt.Errorf("unauthorized: %w", err)
	}

	// Validate the URI is an advertised resource the caller may read; an unknown or
	// admission-denied URI is rejected rather than silently accepted.
	if _, err := s.core.LookupResource(ctx, caller, uri); err != nil {
		return fmt.Errorf("%s: %w", capability, err)
	}
	return nil
}

// completionRefFromMCP converts the mcp-go CompleteParams.Ref (typed as any, holding
// a PromptReference or ResourceReference, possibly decoded as a map from JSON) into a
// domain vmcp.CompletionRef. It accepts both the typed shim structs and the generic
// map[string]any the SDK may hand back after a JSON round-trip.
func completionRefFromMCP(raw any) (vmcp.CompletionRef, error) {
	switch r := raw.(type) {
	case mcp.PromptReference:
		return vmcp.CompletionRef{Type: r.Type, Name: r.Name}, nil
	case mcp.ResourceReference:
		return vmcp.CompletionRef{Type: r.Type, URI: r.URI}, nil
	case map[string]any:
		refType, _ := r["type"].(string)
		name, _ := r["name"].(string)
		uri, _ := r["uri"].(string)
		if refType == "" {
			return vmcp.CompletionRef{}, fmt.Errorf("%w: completion ref missing type", vmcp.ErrInvalidInput)
		}
		return vmcp.CompletionRef{Type: refType, Name: name, URI: uri}, nil
	default:
		return vmcp.CompletionRef{}, fmt.Errorf("%w: unsupported completion ref shape %T", vmcp.ErrInvalidInput, raw)
	}
}

// completionDenyMessage maps a completion ref type to the deny message of the
// underlying capability decision (prompt-get or resource-read), so an admission
// denial on completion reuses the same client-facing wording as a direct get/read.
func completionDenyMessage(refType string) string {
	if refType == vmcp.CompletionRefTypeResource {
		return vmcp.DenyMessageResourceRead
	}
	return vmcp.DenyMessagePromptGet
}

// enforceSessionBinding validates caller against the session's stored identity
// binding. It is the SOLE identity-binding enforcement point on the Serve call
// path: requests reach the core directly, bypassing the BindSession decorator that
// performs this check on MultiSession.CallTool on the legacy path. The SDK's
// SessionIdManager.Validate only gates session existence/termination, not caller
// identity, so without this check a different principal could reuse the session ID.
//
// It fails closed in two ways: a missing session record (already terminated/expired)
// rejects the caller, and ValidateCaller rejects an empty/unparsable binding with
// ErrSessionOwnerUnknown. Unlike the legacy GetAdaptedTools handler, which terminates
// only on ErrUnauthorizedCaller/ErrNilCaller, terminateOnBindingFailure terminates on
// any rejection here — intentional fail-closed behavior, not a bug.
func (s *Server) enforceSessionBinding(ctx context.Context, sessionID string, caller *auth.Identity) error {
	sess, ok := s.vmcpSessionMgr.GetMultiSession(ctx, sessionID)
	if !ok {
		return sessiontypes.ErrUnauthorizedCaller
	}
	// Single-key read: avoid GetMetadata()'s per-call full-map copy on this hot path.
	storedBinding, _ := sess.GetMetadataValue(vmcpsession.MetadataKeyIdentityBinding)
	return vmcpsession.ValidateCaller(storedBinding, caller)
}

// terminateOnBindingFailure logs the hijack-prevention event and terminates the
// session, mirroring GetAdaptedTools' handling of ErrUnauthorizedCaller/ErrNilCaller.
func (s *Server) terminateOnBindingFailure(sessionID, capability string, err error) {
	slog.Warn("caller authorization failed, terminating session",
		"session_id", sessionID, "capability", capability, "error", err)
	if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
		slog.Error("failed to terminate session after auth failure",
			"session_id", sessionID, "error", termErr)
	}
}

// backendDisplayName resolves a logical backend ID to its human-readable name via
// the registry. For a registered backend it records backend.Name — the same value
// the legacy path's WorkloadName carries, so audit events correlate across paths.
//
// For an orphan backend (advertised by the core but absent from the registry) it
// falls back to the raw backendID, so audit records an identifier rather than
// dropping the backend entirely. This does NOT match the legacy path in the orphan
// case: the legacy aggregator's minimal-target fallback leaves WorkloadName empty,
// so legacy records "" there. Recording the ID is the deliberate, arguably-better
// behavior; it is not parity.
func (s *Server) backendDisplayName(ctx context.Context, backendID string) string {
	if backendID == "" {
		return ""
	}
	if backend := s.backendRegistry.Get(ctx, backendID); backend != nil {
		return backend.Name
	}
	return backendID
}

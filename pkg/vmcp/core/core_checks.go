// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"maps"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// CheckToolCall runs the CallTool admission decision without invoking the tool.
// It fetches its own aggregated view (the tool decision sources annotations from
// the advertised set, and composites must be included) and delegates to the shared
// authorizeToolCall helper, so a pre-flight gate and the call path enforce the same
// decision from one source.
//
// The gated allowed-call flow thus aggregates twice (once here, once in CallTool);
// this is intentional and cheap in practice — the Serve-layer CachingAggregator
// (30s TTL) turns the second view into a cache hit (vmcp anti-patterns #8/#9: the
// core does not cache, Serve does). identity is never logged.
func (c *coreVMCP) CheckToolCall(ctx context.Context, identity *auth.Identity, name string, args map[string]any) error {
	// Clone args before handing them to the admission seam, matching CallTool's
	// copy-before-mutating-caller-input discipline (the caller's map — here the
	// parsed request's Arguments, shared via context — must not be mutated).
	args = maps.Clone(args)
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		// An aggregation failure is a transport/plumbing error, NOT an authorization
		// denial — return it unwrapped so the gate admits and the call path surfaces
		// it through existing mapping, rather than converting infra faults into 403s.
		return err
	}
	return c.authorizeToolCall(ctx, identity, name, args, agg)
}

// CheckResourceRead runs the ReadResource admission decision without reading the
// resource. It needs no aggregated view (the URI alone identifies the decision),
// so it works even when the aggregator is failing. identity is never logged.
func (c *coreVMCP) CheckResourceRead(ctx context.Context, identity *auth.Identity, uri string) error {
	return c.authorizeResourceRead(ctx, identity, uri)
}

// CheckPromptGet runs the GetPrompt admission decision without retrieving the
// prompt. It needs no aggregated view (the name alone identifies the decision),
// so it works even when the aggregator is failing. identity is never logged.
func (c *coreVMCP) CheckPromptGet(ctx context.Context, identity *auth.Identity, name string) error {
	return c.authorizePromptGet(ctx, identity, name)
}

// authorizeToolCall runs the CALL-side admission decision for name with args,
// sourcing the tool's annotations from the caller-supplied advertised view (agg)
// so annotation-gated policies evaluate. It returns nil when allowed and an error
// wrapping vmcp.ErrAuthorizationFailed on deny AND on an authorizer error (fail
// closed), classified so the Serve adapter can distinguish it from a transport
// failure via errors.Is — mirroring the live authorizeAndServe. The underlying
// error is preserved in the chain for server-side diagnostics.
//
// This is the single source of truth for the tool-call decision: both CallTool
// (before dispatch) and CheckToolCall (pre-flight) call it with the same agg, so
// the two can never drift. args is treated as read-only.
//
// A name absent from the advertised set carries no annotations, so an
// annotation-gated decision evaluates with no hints (and may deny). In normal
// operation the advertised set and the routing table are derived from the same
// aggregation, so this only arises if they diverge. advertisedTools includes
// composites, so their annotations are sourced too.
func (c *coreVMCP) authorizeToolCall(
	ctx context.Context,
	identity *auth.Identity,
	name string,
	args map[string]any,
	agg *aggregator.AggregatedCapabilities,
) error {
	tool := findAdvertisedTool(c.advertisedTools(agg), name)
	if tool == nil {
		tool = &vmcp.Tool{Name: name}
	}
	if allowed, err := c.admission.AllowToolCall(ctx, identity, tool, args); err != nil {
		return fmt.Errorf("%w: tool %q: %w", vmcp.ErrAuthorizationFailed, name, err)
	} else if !allowed {
		return fmt.Errorf("%w: tool %q", vmcp.ErrAuthorizationFailed, name)
	}
	return nil
}

// authorizeResourceRead runs the read-side admission decision for uri, mirroring
// ListResources' filter for the single read. Resources carry no annotations, so
// the URI alone identifies the decision — it needs no aggregated view. An
// authorizer error fails closed, classified as ErrAuthorizationFailed (see
// authorizeToolCall). Shared by ReadResource and CheckResourceRead.
func (c *coreVMCP) authorizeResourceRead(ctx context.Context, identity *auth.Identity, uri string) error {
	if allowed, err := c.admission.AllowResourceRead(ctx, identity, &vmcp.Resource{URI: uri}); err != nil {
		return fmt.Errorf("%w: resource %q: %w", vmcp.ErrAuthorizationFailed, uri, err)
	} else if !allowed {
		return fmt.Errorf("%w: resource %q", vmcp.ErrAuthorizationFailed, uri)
	}
	return nil
}

// authorizePromptGet runs the get-side admission decision for name, mirroring
// ListPrompts' filter for the single get. Prompts carry no annotations, so the
// name alone identifies the decision — it needs no aggregated view. An authorizer
// error fails closed, classified as ErrAuthorizationFailed (see authorizeToolCall).
// Shared by GetPrompt and CheckPromptGet.
func (c *coreVMCP) authorizePromptGet(ctx context.Context, identity *auth.Identity, name string) error {
	if allowed, err := c.admission.AllowPromptGet(ctx, identity, &vmcp.Prompt{Name: name}); err != nil {
		return fmt.Errorf("%w: prompt %q: %w", vmcp.ErrAuthorizationFailed, name, err)
	} else if !allowed {
		return fmt.Errorf("%w: prompt %q", vmcp.ErrAuthorizationFailed, name)
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ratelimit applies rate limiting at the vMCP domain boundary.
package ratelimit

import (
	"context"
	"errors"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/auth"
	baseratelimit "github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// decorator wraps a [core.VMCP] to rate-limit tool calls. Every method except
// CallTool is promoted from the embedded inner core unchanged. The decorator sits
// below the session optimizer, so name is already the resolved backend tool name.
//
// CheckToolCall is deliberately NOT overridden: promoting it to inner is correct
// because a pre-flight admission check must not consume a rate-limit token — the
// limiter applies only when the call is actually dispatched via CallTool.
type decorator struct {
	core.VMCP
	limiter baseratelimit.Limiter
}

var _ core.VMCP = (*decorator)(nil)

// NewDecorator wraps inner with vMCP rate limiting.
//
// inner must be non-nil; a nil inner is a composition-root wiring bug and panics
// rather than deferring the failure to the first promoted method call. A nil
// limiter means rate limiting is disabled and inner is returned unchanged.
func NewDecorator(inner core.VMCP, limiter baseratelimit.Limiter) core.VMCP {
	if inner == nil {
		panic("ratelimit: NewDecorator requires a non-nil inner VMCP")
	}
	if limiter == nil {
		return inner
	}
	return &decorator{
		VMCP:    inner,
		limiter: limiter,
	}
}

// CallTool checks the rate limit for name before delegating to inner. At this
// seam name is already resolved by any outer optimizer layer, so per-tool bucket
// keys match the real backend tool instead of the optimizer call_tool meta-tool.
func (d *decorator) CallTool(
	ctx context.Context, identity *auth.Identity, name string,
	args map[string]any, meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	if err := baseratelimit.Allow(ctx, d.limiter, identity, name); err != nil {
		var limited *baseratelimit.RateLimitedError
		if errors.As(err, &limited) {
			return nil, err
		}
		slog.WarnContext(ctx, "rate limit check failed, allowing tool call", "tool", name, "error", err)
	}
	return d.VMCP.CallTool(ctx, identity, name, args, meta)
}

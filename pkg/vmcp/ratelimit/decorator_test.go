// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	baseratelimit "github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

type recordingLimiter struct {
	toolName string
	userID   string
	decision *baseratelimit.Decision
	err      error
}

func (l *recordingLimiter) Allow(_ context.Context, toolName, userID string) (*baseratelimit.Decision, error) {
	l.toolName = toolName
	l.userID = userID
	if l.err != nil {
		return nil, l.err
	}
	if l.decision != nil {
		return l.decision, nil
	}
	return &baseratelimit.Decision{Allowed: true}, nil
}

type recordingCore struct {
	core.VMCP
	called      bool
	checkCalled bool
	identity    *auth.Identity
	name        string
	args        map[string]any
	meta        map[string]any
	result      *vmcp.ToolCallResult
	err         error
}

func (c *recordingCore) CheckToolCall(_ context.Context, _ *auth.Identity, _ string, _ map[string]any) error {
	c.checkCalled = true
	return nil
}

func (c *recordingCore) CallTool(
	_ context.Context,
	identity *auth.Identity,
	name string,
	args map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	c.called = true
	c.identity = identity
	c.name = name
	c.args = args
	c.meta = meta
	if c.result != nil || c.err != nil {
		return c.result, c.err
	}
	return &vmcp.ToolCallResult{StructuredContent: map[string]any{"ok": true}}, nil
}

func TestNewDecoratorNilInnerPanics(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		NewDecorator(nil, &recordingLimiter{})
	})
}

func TestNewDecoratorNilLimiterReturnsInner(t *testing.T) {
	t.Parallel()

	inner := &recordingCore{}

	got := NewDecorator(inner, nil)

	assert.Same(t, inner, got)
}

func TestCallToolAllowedDelegatesWithResolvedToolName(t *testing.T) {
	t.Parallel()

	limiter := &recordingLimiter{}
	inner := &recordingCore{}
	decorated := NewDecorator(inner, limiter)
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}}
	args := map[string]any{"message": "hello"}
	meta := map[string]any{"trace": "abc"}

	result, err := decorated.CallTool(t.Context(), identity, "backend_a_echo", args, meta)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, inner.called)
	assert.Same(t, identity, inner.identity)
	assert.Equal(t, "backend_a_echo", inner.name)
	assert.Equal(t, args, inner.args)
	assert.Equal(t, meta, inner.meta)
	assert.Equal(t, "backend_a_echo", limiter.toolName)
	assert.Equal(t, "alice", limiter.userID)
}

func TestCallToolRateLimitedDoesNotDelegate(t *testing.T) {
	t.Parallel()

	limiter := &recordingLimiter{
		decision: &baseratelimit.Decision{
			Allowed:    false,
			RetryAfter: 5 * time.Second,
		},
	}
	inner := &recordingCore{}
	decorated := NewDecorator(inner, limiter)

	result, err := decorated.CallTool(t.Context(), nil, "backend_a_echo", nil, nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.False(t, inner.called)
	var limited *baseratelimit.RateLimitedError
	require.ErrorAs(t, err, &limited)
	assert.Equal(t, 5*time.Second, limited.RetryAfter)
}

func TestCallToolLimiterErrorFailsOpen(t *testing.T) {
	t.Parallel()

	expected := errors.New("redis unavailable")
	limiter := &recordingLimiter{err: expected}
	inner := &recordingCore{}
	decorated := NewDecorator(inner, limiter)

	result, err := decorated.CallTool(t.Context(), nil, "backend_a_echo", nil, nil)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, inner.called)
}

func TestCallToolNilIdentityUsesEmptyUserID(t *testing.T) {
	t.Parallel()

	limiter := &recordingLimiter{}
	inner := &recordingCore{}
	decorated := NewDecorator(inner, limiter)

	_, err := decorated.CallTool(t.Context(), nil, "backend_a_echo", nil, nil)

	require.NoError(t, err)
	assert.True(t, inner.called)
	assert.Empty(t, limiter.userID)
}

// TestCheckToolCallConsumesNoTokens verifies the deliberate non-override of
// CheckToolCall: a pre-flight admission check promotes straight to inner without
// touching the limiter, so it never consumes a rate-limit token (the limiter
// applies only at CallTool dispatch).
func TestCheckToolCallConsumesNoTokens(t *testing.T) {
	t.Parallel()

	limiter := &recordingLimiter{}
	inner := &recordingCore{}
	decorated := NewDecorator(inner, limiter)

	err := decorated.CheckToolCall(t.Context(), nil, "backend_a_echo", nil)

	require.NoError(t, err)
	assert.True(t, inner.checkCalled, "the check must promote to inner")
	assert.Empty(t, limiter.toolName, "a pre-flight check must not consume a rate-limit token")
}

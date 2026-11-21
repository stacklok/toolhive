// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
)

// mockAuthorizer is a hand-rolled stub (no generated mock exists for
// authorizers.Authorizer). The single-method interface makes a simple stub
// more readable than gomock boilerplate for configurable per-tool results.
type mockAuthorizer struct {
	// results maps tool name -> (authorized, error).
	results map[string]mockResult
	// calls records each invocation in order.
	calls []mockCall
	// allowed, when set, decides by operation and takes precedence over results.
	allowed map[authorizers.MCPOperation]bool
	// callByResource and readByResource, when set, decide tool:call and tool:read
	// per tool name.
	callByResource map[string]bool
	readByResource map[string]bool
	// blockRead makes tool:read wait for its context to end, like a hung PDP.
	blockRead bool
	// reads counts the tool:read checks received.
	reads atomic.Int32
}

type mockResult struct {
	authorized bool
	err        error
}

type mockCall struct {
	feature    authorizers.MCPFeature
	operation  authorizers.MCPOperation
	resourceID string
}

func (m *mockAuthorizer) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature authorizers.MCPFeature,
	operation authorizers.MCPOperation,
	resourceID string,
	_ map[string]interface{},
) (bool, error) {
	m.calls = append(m.calls, mockCall{
		feature:    feature,
		operation:  operation,
		resourceID: resourceID,
	})
	if operation == authorizers.MCPOperationRead {
		m.reads.Add(1)
		if m.blockRead {
			<-ctx.Done()
			return false, ctx.Err()
		}
		if m.readByResource != nil {
			return m.readByResource[resourceID], nil
		}
	}
	if operation == authorizers.MCPOperationCall && m.callByResource != nil {
		return m.callByResource[resourceID], nil
	}
	if m.allowed != nil {
		return m.allowed[operation], nil
	}
	r, ok := m.results[resourceID]
	if !ok {
		return false, nil
	}
	return r.authorized, r.err
}

func makeTool(name string, ann *mcp.ToolAnnotation) mcp.Tool {
	t := mcp.Tool{Name: name}
	if ann != nil {
		t.Annotations = *ann
	}
	return t
}

func TestAuthorizedToolIndexes(t *testing.T) {
	t.Parallel()

	errAuth := errors.New("authorization failure")

	tests := []struct {
		name       string
		authorizer authorizers.Authorizer
		tools      []mcp.Tool
		wantNames  []string
	}{
		{
			name: "tool:read alone keeps every tool",
			authorizer: &mockAuthorizer{
				allowed: map[authorizers.MCPOperation]bool{authorizers.MCPOperationRead: true},
			},
			tools:     []mcp.Tool{makeTool("alpha", nil), makeTool("beta", nil)},
			wantNames: []string{"alpha", "beta"},
		},
		{
			name:       "nil authorizer keeps every tool",
			authorizer: nil,
			tools: []mcp.Tool{
				makeTool("alpha", nil),
				makeTool("beta", nil),
			},
			wantNames: []string{"alpha", "beta"},
		},
		{
			name:       "empty tool list keeps nothing",
			authorizer: &mockAuthorizer{results: map[string]mockResult{}},
			tools:      []mcp.Tool{},
			wantNames:  []string{},
		},
		{
			name: "filters out denied tools",
			authorizer: &mockAuthorizer{results: map[string]mockResult{
				"allowed":  {authorized: true},
				"denied":   {authorized: false},
				"allowed2": {authorized: true},
			}},
			tools: []mcp.Tool{
				makeTool("allowed", nil),
				makeTool("denied", nil),
				makeTool("allowed2", nil),
			},
			wantNames: []string{"allowed", "allowed2"},
		},
		{
			name: "skips tools where authorizer returns error",
			authorizer: &mockAuthorizer{results: map[string]mockResult{
				"good":  {authorized: true},
				"error": {err: errAuth},
			}},
			tools: []mcp.Tool{
				makeTool("good", nil),
				makeTool("error", nil),
			},
			wantNames: []string{"good"},
		},
		{
			name: "tools with annotations are still filtered",
			authorizer: &mockAuthorizer{results: map[string]mockResult{
				"readonly": {authorized: true},
				"writable": {authorized: false},
			}},
			tools: []mcp.Tool{
				makeTool("readonly", &mcp.ToolAnnotation{ReadOnlyHint: boolPtr(true)}),
				makeTool("writable", &mcp.ToolAnnotation{DestructiveHint: boolPtr(true)}),
			},
			wantNames: []string{"readonly"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			indexes := authorizedToolIndexes(context.Background(), authorizers.WithToolCallImpliesRead(tc.authorizer), tc.tools)

			gotNames := make([]string, len(indexes))
			for i, index := range indexes {
				gotNames[i] = tc.tools[index].Name
			}
			assert.Equal(t, tc.wantNames, gotNames)
		})
	}
}

func TestAuthorizedToolIndexes_CallsAuthorizerCorrectly(t *testing.T) {
	t.Parallel()

	mock := &mockAuthorizer{results: map[string]mockResult{
		"tool1": {authorized: true},
	}}

	tools := []mcp.Tool{makeTool("tool1", nil)}
	authorizedToolIndexes(context.Background(), mock, tools)

	require.Len(t, mock.calls, 1)
	assert.Equal(t, authorizers.MCPFeatureTool, mock.calls[0].feature)
	assert.Equal(t, authorizers.MCPOperationRead, mock.calls[0].operation)
	assert.Equal(t, "tool1", mock.calls[0].resourceID)
}

// cedarCtx returns a context with an Identity attached, as the Cedar authorizer expects.
func cedarCtx(t *testing.T) context.Context {
	t.Helper()
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user123",
		Name:    "Test User",
		Claims:  map[string]interface{}{"sub": "user123", "name": "Test User"},
	}}
	return auth.WithIdentity(context.Background(), identity)
}

func TestAuthorizedToolIndexes_WithCedarAuthorizer(t *testing.T) {
	t.Parallel()

	rawCedar, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	cedarAuth := authorizers.WithToolCallImpliesRead(rawCedar)

	t.Run("keeps only permitted tool", func(t *testing.T) {
		t.Parallel()

		tools := []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "calculator", Description: "Perform calculations"},
			{Name: "translator", Description: "Translate text"},
		}

		assert.Equal(t, []int{0}, authorizedToolIndexes(cedarCtx(t), cedarAuth, tools))
	})

	t.Run("returns empty list when no tools are permitted", func(t *testing.T) {
		t.Parallel()

		tools := []mcp.Tool{
			{Name: "calculator", Description: "Perform calculations"},
			{Name: "translator", Description: "Translate text"},
		}

		assert.Empty(t, authorizedToolIndexes(cedarCtx(t), cedarAuth, tools))
	})
}

func TestAuthorizeToolRead_WithCedarAuthorizer(t *testing.T) {
	t.Parallel()

	rawCedar, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	cedarAuth := authorizers.WithToolCallImpliesRead(rawCedar)

	t.Run("permits authorized tool", func(t *testing.T) {
		t.Parallel()

		ok, err := authorizeToolRead(cedarCtx(t), cedarAuth, "weather", nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("denies unauthorized tool", func(t *testing.T) {
		t.Parallel()

		ok, err := authorizeToolRead(cedarCtx(t), cedarAuth, "calculator", nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestAuthorizeToolRead_WithArguments(t *testing.T) {
	t.Parallel()

	// Policy that permits call_tool only when the arg_mode argument equals "safe".
	// Cedar sees arguments with an "arg_" prefix (preprocessed by the authorizer).
	rawCedar, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"deploy") when { context.arg_mode == "safe" };`,
		},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	cedarAuth := authorizers.WithToolCallImpliesRead(rawCedar)

	t.Run("permits when arguments satisfy policy", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{"mode": "safe"}
		ok, err := authorizeToolRead(cedarCtx(t), cedarAuth, "deploy", args)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("denies when arguments do not satisfy policy", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{"mode": "dangerous"}
		ok, err := authorizeToolRead(cedarCtx(t), cedarAuth, "deploy", args)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("denies when arguments are missing", func(t *testing.T) {
		t.Parallel()

		// When the policy references an argument that isn't present,
		// Cedar returns an evaluation error. The caller should treat
		// this as denied access.
		ok, err := authorizeToolRead(cedarCtx(t), cedarAuth, "deploy", nil)
		assert.False(t, ok)
		// Cedar may return an error when evaluating a when-clause against
		// missing context attributes — either outcome (error or clean deny)
		// is acceptable as long as authorized is false.
		_ = err
	})
}

func TestAuthorizeToolRead(t *testing.T) {
	t.Parallel()

	errAuth := errors.New("auth error")

	tests := []struct {
		name       string
		authorizer authorizers.Authorizer
		toolName   string
		wantOK     bool
		wantErr    error
	}{
		{
			name:       "nil authorizer returns true",
			authorizer: nil,
			toolName:   "anything",
			wantOK:     true,
			wantErr:    nil,
		},
		{
			name: "delegates to authorizer and returns allowed",
			authorizer: &mockAuthorizer{results: map[string]mockResult{
				"mytool": {authorized: true},
			}},
			toolName: "mytool",
			wantOK:   true,
			wantErr:  nil,
		},
		{
			name: "delegates to authorizer and returns denied",
			authorizer: &mockAuthorizer{results: map[string]mockResult{
				"blocked": {authorized: false},
			}},
			toolName: "blocked",
			wantOK:   false,
			wantErr:  nil,
		},
		{
			name: "propagates authorizer errors",
			authorizer: &mockAuthorizer{results: map[string]mockResult{
				"failing": {err: errAuth},
			}},
			toolName: "failing",
			wantOK:   false,
			wantErr:  errAuth,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ok, err := authorizeToolRead(context.Background(), tc.authorizer, tc.toolName, nil)

			assert.Equal(t, tc.wantOK, ok)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAuthorizedToolIndexes_SharedDeadlineStopsTheLoop(t *testing.T) {
	t.Parallel()

	tools := []mcp.Tool{makeTool("alpha", nil), makeTool("beta", nil), makeTool("gamma", nil)}
	inner := &mockAuthorizer{
		allowed:   map[authorizers.MCPOperation]bool{authorizers.MCPOperationCall: true},
		blockRead: true,
	}
	// The outer timeout only keeps this test from hanging if the shared deadline is removed.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	got := authorizedToolIndexesWithin(ctx, authorizers.WithToolCallImpliesRead(inner), tools, 50*time.Millisecond)

	assert.Empty(t, got, "no tool may be listed through the tool:call fallback after a timeout")
	assert.Equal(t, int32(1), inner.reads.Load(), "the loop must stop at the first expired check")
	assert.Less(t, time.Since(start), 2*time.Second, "the shared deadline must end the filter early")
}

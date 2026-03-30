// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	_ context.Context,
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

func TestFilterToolsByPolicy(t *testing.T) {
	t.Parallel()

	errAuth := errors.New("authorization failure")

	tests := []struct {
		name       string
		authorizer authorizers.Authorizer
		tools      []mcp.Tool
		wantNames  []string
	}{
		{
			name:       "nil authorizer returns all tools unchanged",
			authorizer: nil,
			tools: []mcp.Tool{
				makeTool("alpha", nil),
				makeTool("beta", nil),
			},
			wantNames: []string{"alpha", "beta"},
		},
		{
			name:       "empty tool list returns empty",
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

			got := filterToolsByPolicy(context.Background(), tc.authorizer, tc.tools)

			gotNames := make([]string, len(got))
			for i, tool := range got {
				gotNames[i] = tool.Name
			}
			assert.Equal(t, tc.wantNames, gotNames)
		})
	}
}

func TestFilterToolsByPolicy_CallsAuthorizerCorrectly(t *testing.T) {
	t.Parallel()

	mock := &mockAuthorizer{results: map[string]mockResult{
		"tool1": {authorized: true},
	}}

	tools := []mcp.Tool{makeTool("tool1", nil)}
	filterToolsByPolicy(context.Background(), mock, tools)

	require.Len(t, mock.calls, 1)
	assert.Equal(t, authorizers.MCPFeatureTool, mock.calls[0].feature)
	assert.Equal(t, authorizers.MCPOperationCall, mock.calls[0].operation)
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

func TestFilterToolsByPolicy_WithCedarAuthorizer(t *testing.T) {
	t.Parallel()

	cedarAuth, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err)

	t.Run("keeps only permitted tool", func(t *testing.T) {
		t.Parallel()

		tools := []mcp.Tool{
			{Name: "weather", Description: "Get weather information"},
			{Name: "calculator", Description: "Perform calculations"},
			{Name: "translator", Description: "Translate text"},
		}

		got := filterToolsByPolicy(cedarCtx(t), cedarAuth, tools)

		require.Len(t, got, 1)
		assert.Equal(t, "weather", got[0].Name)
		assert.Equal(t, "Get weather information", got[0].Description)
	})

	t.Run("returns empty list when no tools are permitted", func(t *testing.T) {
		t.Parallel()

		tools := []mcp.Tool{
			{Name: "calculator", Description: "Perform calculations"},
			{Name: "translator", Description: "Translate text"},
		}

		got := filterToolsByPolicy(cedarCtx(t), cedarAuth, tools)

		assert.Empty(t, got)
	})
}

func TestAuthorizeToolCall_WithCedarAuthorizer(t *testing.T) {
	t.Parallel()

	cedarAuth, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err)

	t.Run("permits authorized tool", func(t *testing.T) {
		t.Parallel()

		ok, err := authorizeToolCall(cedarCtx(t), cedarAuth, "weather", nil)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("denies unauthorized tool", func(t *testing.T) {
		t.Parallel()

		ok, err := authorizeToolCall(cedarCtx(t), cedarAuth, "calculator", nil)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestAuthorizeToolCall_WithArguments(t *testing.T) {
	t.Parallel()

	// Policy that permits call_tool only when the arg_mode argument equals "safe".
	// Cedar sees arguments with an "arg_" prefix (preprocessed by the authorizer).
	cedarAuth, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"deploy") when { context.arg_mode == "safe" };`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err)

	t.Run("permits when arguments satisfy policy", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{"mode": "safe"}
		ok, err := authorizeToolCall(cedarCtx(t), cedarAuth, "deploy", args)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("denies when arguments do not satisfy policy", func(t *testing.T) {
		t.Parallel()

		args := map[string]interface{}{"mode": "dangerous"}
		ok, err := authorizeToolCall(cedarCtx(t), cedarAuth, "deploy", args)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("denies when arguments are missing", func(t *testing.T) {
		t.Parallel()

		// When the policy references an argument that isn't present,
		// Cedar returns an evaluation error. The caller should treat
		// this as denied access.
		ok, err := authorizeToolCall(cedarCtx(t), cedarAuth, "deploy", nil)
		assert.False(t, ok)
		// Cedar may return an error when evaluating a when-clause against
		// missing context attributes — either outcome (error or clean deny)
		// is acceptable as long as authorized is false.
		_ = err
	})
}

func TestAuthorizeToolCall(t *testing.T) {
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

			ok, err := authorizeToolCall(context.Background(), tc.authorizer, tc.toolName, nil)

			assert.Equal(t, tc.wantOK, ok)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

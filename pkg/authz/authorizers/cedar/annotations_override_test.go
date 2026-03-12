// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// TestAnnotationAttributesCannotOverrideStandardAttributes verifies that the
// standard Cedar resource attributes ("name", "operation", "feature") retain
// their correct values even when tool annotations are present in the context.
//
// The merge order in authorizeToolCall places annotation attributes first and
// standard attributes last, so standard keys always win. This test proves that
// invariant by using Cedar policies whose `when` clauses assert the expected
// values of the standard attributes.
func TestAnnotationAttributesCannotOverrideStandardAttributes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		policies         []string
		toolName         string
		annotations      *authorizers.ToolAnnotations
		expectAuthorized bool
	}{
		{
			name: "resource.name equals the tool name despite annotations",
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"correct_tool"
			)
			when {
				resource.name == "correct_tool"
			};
			`},
			toolName:         "correct_tool",
			annotations:      &authorizers.ToolAnnotations{ReadOnlyHint: testBoolPtr(true)},
			expectAuthorized: true,
		},
		{
			name: "resource.operation equals call despite annotations",
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"op_tool"
			)
			when {
				resource.operation == "call"
			};
			`},
			toolName:         "op_tool",
			annotations:      &authorizers.ToolAnnotations{DestructiveHint: testBoolPtr(false)},
			expectAuthorized: true,
		},
		{
			name: "resource.feature equals tool despite annotations",
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"feat_tool"
			)
			when {
				resource.feature == "tool"
			};
			`},
			toolName:         "feat_tool",
			annotations:      &authorizers.ToolAnnotations{IdempotentHint: testBoolPtr(true)},
			expectAuthorized: true,
		},
		{
			name: "all standard attributes correct with all annotations set",
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"full_tool"
			)
			when {
				resource.name == "full_tool" &&
				resource.operation == "call" &&
				resource.feature == "tool"
			};
			`},
			toolName: "full_tool",
			annotations: &authorizers.ToolAnnotations{
				ReadOnlyHint:    testBoolPtr(true),
				DestructiveHint: testBoolPtr(false),
				IdempotentHint:  testBoolPtr(true),
				OpenWorldHint:   testBoolPtr(false),
			},
			expectAuthorized: true,
		},
		{
			name: "policy checking wrong name is denied even with annotations",
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"actual_tool"
			)
			when {
				resource.name == "wrong_tool"
			};
			`},
			toolName:         "actual_tool",
			annotations:      &authorizers.ToolAnnotations{ReadOnlyHint: testBoolPtr(true)},
			expectAuthorized: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			authzr, err := NewCedarAuthorizer(ConfigOptions{
				Policies:     tc.policies,
				EntitiesJSON: `[]`,
			})
			require.NoError(t, err, "Failed to create Cedar authorizer")

			ctx := t.Context()
			claims := jwt.MapClaims{
				"sub":  "user456",
				"name": "Annotation Tester",
			}
			identity := &auth.Identity{Subject: "user456", Claims: claims}
			ctx = auth.WithIdentity(ctx, identity)

			if tc.annotations != nil {
				ctx = authorizers.WithToolAnnotations(ctx, tc.annotations)
			}

			authorized, err := authzr.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				tc.toolName,
				nil,
			)

			if tc.expectAuthorized {
				require.NoError(t, err, "Authorization should not error")
				assert.True(t, authorized, "Expected authorization to succeed")
			} else {
				if err != nil {
					// Cedar evaluation error is acceptable for deny
					return
				}
				assert.False(t, authorized, "Expected authorization to be denied")
			}
		})
	}
}

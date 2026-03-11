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

func testBoolPtr(b bool) *bool { return &b }

// TestAuthorizeWithToolAnnotations verifies that tool annotations stored in
// context are forwarded to the Cedar authorizer as resource entity attributes,
// enabling policies like "resource.readOnlyHint == true".
func TestAuthorizeWithToolAnnotations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		policies         []string
		resourceID       string
		annotations      *authorizers.ToolAnnotations
		expectAuthorized bool
	}{
		{
			name: "readOnlyHint true allowed by annotation policy",
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"readonly_tool"
			)
			when {
				resource.readOnlyHint == true
			};
			`},
			resourceID:       "readonly_tool",
			annotations:      &authorizers.ToolAnnotations{ReadOnlyHint: testBoolPtr(true)},
			expectAuthorized: true,
		},
		{
			name: "destructiveHint true denied by forbid policy",
			// The permit allows everything, but the forbid blocks destructive tools.
			// Each policy must be a separate string for Cedar parsing.
			policies: []string{
				`permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"dangerous_tool"
			);`,
				`forbid(
				principal,
				action == Action::"call_tool",
				resource == Tool::"dangerous_tool"
			)
			when {
				resource.destructiveHint == true
			};`,
			},
			resourceID:       "dangerous_tool",
			annotations:      &authorizers.ToolAnnotations{DestructiveHint: testBoolPtr(true)},
			expectAuthorized: false,
		},
		{
			name: "no annotations gracefully degrades - policy requiring annotation does not match",
			// This policy requires readOnlyHint to be true on the resource.
			// Without annotations in context, the attribute won't exist and
			// Cedar will produce an evaluation error for the `when` clause,
			// which means the policy does not apply and the default is deny.
			policies: []string{`
			permit(
				principal,
				action == Action::"call_tool",
				resource == Tool::"some_tool"
			)
			when {
				resource.readOnlyHint == true
			};
			`},
			resourceID:       "some_tool",
			annotations:      nil,
			expectAuthorized: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a Cedar authorizer with the test policies
			authzr, err := NewCedarAuthorizer(ConfigOptions{
				Policies:     tc.policies,
				EntitiesJSON: `[]`,
			})
			require.NoError(t, err, "Failed to create Cedar authorizer")

			// Build context with identity
			ctx := t.Context()
			claims := jwt.MapClaims{
				"sub":  "user123",
				"name": "Test User",
			}
			identity := &auth.Identity{Subject: "user123", Claims: claims}
			ctx = auth.WithIdentity(ctx, identity)

			// Add annotations to context if provided
			if tc.annotations != nil {
				ctx = authorizers.WithToolAnnotations(ctx, tc.annotations)
			}

			authorized, err := authzr.AuthorizeWithJWTClaims(
				ctx,
				authorizers.MCPFeatureTool,
				authorizers.MCPOperationCall,
				tc.resourceID,
				nil,
			)

			// The "no annotations" case may produce a Cedar evaluation error
			// because the policy references an attribute that doesn't exist.
			// Cedar treats such errors as the policy not applying, which
			// results in a default deny. We check both possibilities.
			if tc.expectAuthorized {
				require.NoError(t, err, "Authorization should not error")
				assert.True(t, authorized, "Expected authorization to succeed")
			} else {
				// Either err != nil (Cedar evaluation error) or authorized == false
				if err != nil {
					// This is acceptable - Cedar evaluation error means deny
					return
				}
				assert.False(t, authorized, "Expected authorization to be denied")
			}
		})
	}
}

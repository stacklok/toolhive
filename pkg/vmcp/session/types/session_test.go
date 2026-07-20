// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/auth"
)

func TestShouldAllowAnonymous(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		identity *auth.Identity
		want     bool
	}{
		{
			name:     "nil identity",
			identity: nil,
			want:     true,
		},
		{
			name: "token present",
			identity: &auth.Identity{
				Token: "x",
				PrincipalInfo: auth.PrincipalInfo{
					Claims: map[string]any{"iss": "https://idp.example", "sub": "alice"},
				},
			},
			want: false,
		},
		{
			name: "empty token with valid iss+sub claims",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{
					Claims: map[string]any{"iss": "toolhive-local", "sub": "alice"},
				},
			},
			want: false,
		},
		{
			name: "empty token and nil claims",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: nil},
			},
			want: true,
		},
		{
			name: "empty token and missing iss",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"sub": "x"}},
			},
			want: true,
		},
		{
			name: "empty token and missing sub",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": "x"}},
			},
			want: true,
		},
		{
			name: "empty token and empty string iss and sub",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": "", "sub": ""}},
			},
			want: true,
		},
		{
			// Fail-closed: non-string claim from a misbehaving validator → bound, not anonymous.
			name: "non-string iss fails closed",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": 42, "sub": "x"}},
			},
			want: false,
		},
		{
			name: "non-string sub fails closed",
			identity: &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": "x", "sub": []string{"foo"}}},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ShouldAllowAnonymous(tt.identity))
		})
	}
}

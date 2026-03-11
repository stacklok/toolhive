// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package security_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/auth"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// TestShouldAllowAnonymous_EdgeCases tests the ShouldAllowAnonymous helper.
func TestShouldAllowAnonymous_EdgeCases(t *testing.T) {
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
			name:     "non-nil identity with token",
			identity: &auth.Identity{Subject: "user", Token: "token"},
			want:     false,
		},
		{
			name:     "non-nil identity with empty token",
			identity: &auth.Identity{Subject: "user", Token: ""},
			want:     true, // Empty token is treated as anonymous
		},
		{
			name:     "non-nil identity with empty subject",
			identity: &auth.Identity{Subject: "", Token: "token"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sessiontypes.ShouldAllowAnonymous(tt.identity)
			assert.Equal(t, tt.want, got)
		})
	}
}

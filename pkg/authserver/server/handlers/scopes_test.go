// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
)

func TestUnionScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      []string
		baseline []string
		want     []string
	}{
		{
			name:     "both nil returns nil",
			req:      nil,
			baseline: nil,
			want:     nil,
		},
		{
			name:     "both empty returns nil",
			req:      []string{},
			baseline: []string{},
			want:     nil,
		},
		{
			name:     "nil requested empty baseline returns nil",
			req:      nil,
			baseline: []string{},
			want:     nil,
		},
		{
			name:     "requested only preserved unchanged",
			req:      []string{"openid", "profile"},
			baseline: nil,
			want:     []string{"openid", "profile"},
		},
		{
			name:     "baseline only returned when no requested",
			req:      nil,
			baseline: []string{"openid", "email"},
			want:     []string{"openid", "email"},
		},
		{
			name:     "requested subset of baseline: requested first then non-overlapping baseline",
			req:      []string{"openid"},
			baseline: []string{"openid", "profile", "email"},
			want:     []string{"openid", "profile", "email"},
		},
		{
			name:     "disjoint sets: requested first then baseline",
			req:      []string{"openid", "profile"},
			baseline: []string{"email", "offline_access"},
			want:     []string{"openid", "profile", "email", "offline_access"},
		},
		{
			name:     "exact match produces no duplicates",
			req:      []string{"openid", "profile"},
			baseline: []string{"openid", "profile"},
			want:     []string{"openid", "profile"},
		},
		{
			name:     "duplicates in requested are deduplicated requested-first order preserved",
			req:      []string{"openid", "openid", "profile"},
			baseline: nil,
			want:     []string{"openid", "profile"},
		},
		{
			name:     "duplicates in baseline are deduplicated",
			req:      nil,
			baseline: []string{"openid", "profile", "openid"},
			want:     []string{"openid", "profile"},
		},
		{
			name:     "duplicates in both are deduplicated with requested-first order",
			req:      []string{"openid", "openid", "profile"},
			baseline: []string{"profile", "email", "email"},
			want:     []string{"openid", "profile", "email"},
		},
		{
			name:     "no expansion when baseline is subset of requested (WARN not triggered in handler)",
			req:      []string{"openid", "profile", "email"},
			baseline: []string{"openid", "profile"},
			want:     []string{"openid", "profile", "email"},
		},
		{
			name:     "multi-element requested with strict-superset baseline preserves requested order then appends",
			req:      []string{"openid", "profile"},
			baseline: []string{"openid", "profile", "email", "offline_access"},
			want:     []string{"openid", "profile", "email", "offline_access"},
		},
		{
			name:     "empty-string entry in requested is passed through unchanged (precondition: caller must validate)",
			req:      []string{"", "openid"},
			baseline: nil,
			want:     []string{"", "openid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := registration.UnionScopes(tt.req, tt.baseline)
			assert.Equal(t, tt.want, got)
		})
	}
}

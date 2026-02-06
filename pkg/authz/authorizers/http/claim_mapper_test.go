// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"reflect"
	"testing"
)

func TestMPEClaimMapper_MapClaims(t *testing.T) {
	t.Parallel()

	mapper := &MPEClaimMapper{}
	emptyAnnotations := make(map[string]any)

	tests := []struct {
		name   string
		claims map[string]any
		want   map[string]any
	}{
		{
			name:   "nil claims",
			claims: nil,
			want:   map[string]any{},
		},
		{
			name: "basic claims",
			claims: map[string]any{
				"sub": "user@example.com",
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "mroles claim (MPE native)",
			claims: map[string]any{
				"sub":    "user@example.com",
				"mroles": []string{"developer"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []string{"developer"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "roles mapped to mroles",
			claims: map[string]any{
				"sub":   "user@example.com",
				"roles": []string{"admin"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []string{"admin"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "mgroups claim (MPE native)",
			claims: map[string]any{
				"sub":     "user@example.com",
				"mgroups": []string{"engineering"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mgroups":      []string{"engineering"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "groups mapped to mgroups",
			claims: map[string]any{
				"sub":    "user@example.com",
				"groups": []string{"engineering"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mgroups":      []string{"engineering"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "scopes claim",
			claims: map[string]any{
				"sub":    "user@example.com",
				"scopes": []string{"read", "write"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"scopes":       []string{"read", "write"},
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "scope mapped to scopes",
			claims: map[string]any{
				"sub":   "user@example.com",
				"scope": "read write",
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"scopes":       "read write",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "mclearance claim (MPE native)",
			claims: map[string]any{
				"sub":        "user@example.com",
				"mclearance": "TOP_SECRET",
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mclearance":   "TOP_SECRET",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "clearance mapped to mclearance",
			claims: map[string]any{
				"sub":       "user@example.com",
				"clearance": "SECRET",
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mclearance":   "SECRET",
				"mannotations": emptyAnnotations,
			},
		},
		{
			name: "mannotations claim (MPE native)",
			claims: map[string]any{
				"sub":          "user@example.com",
				"mannotations": map[string]string{"dept": "engineering"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mannotations": map[string]string{"dept": "engineering"},
			},
		},
		{
			name: "annotations mapped to mannotations",
			claims: map[string]any{
				"sub":         "user@example.com",
				"annotations": map[string]string{"dept": "sales"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mannotations": map[string]string{"dept": "sales"},
			},
		},
		{
			name: "all claims",
			claims: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []string{"admin"},
				"mgroups":      []string{"engineering"},
				"scopes":       []string{"read", "write"},
				"mclearance":   "TOP_SECRET",
				"mannotations": map[string]string{"dept": "engineering"},
			},
			want: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []string{"admin"},
				"mgroups":      []string{"engineering"},
				"scopes":       []string{"read", "write"},
				"mclearance":   "TOP_SECRET",
				"mannotations": map[string]string{"dept": "engineering"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mapper.MapClaims(tt.claims)

			// Check expected fields
			for k, v := range tt.want {
				if !reflect.DeepEqual(got[k], v) {
					t.Errorf("MPEClaimMapper.MapClaims()[%s] = %v, want %v", k, got[k], v)
				}
			}

			// Verify mannotations exists (required for some PDPs in identity phase)
			if tt.claims != nil {
				if _, ok := got["mannotations"]; !ok {
					t.Error("MPEClaimMapper.MapClaims() missing mannotations field")
				}
			}
		})
	}
}

func TestStandardClaimMapper_MapClaims(t *testing.T) {
	t.Parallel()

	mapper := &StandardClaimMapper{}

	tests := []struct {
		name   string
		claims map[string]any
		want   map[string]any
	}{
		{
			name:   "nil claims",
			claims: nil,
			want:   map[string]any{},
		},
		{
			name: "basic claims",
			claims: map[string]any{
				"sub": "user@example.com",
			},
			want: map[string]any{
				"sub": "user@example.com",
			},
		},
		{
			name: "roles claim",
			claims: map[string]any{
				"sub":   "user@example.com",
				"roles": []string{"developer"},
			},
			want: map[string]any{
				"sub":   "user@example.com",
				"roles": []string{"developer"},
			},
		},
		{
			name: "groups claim",
			claims: map[string]any{
				"sub":    "user@example.com",
				"groups": []string{"engineering"},
			},
			want: map[string]any{
				"sub":    "user@example.com",
				"groups": []string{"engineering"},
			},
		},
		{
			name: "scopes claim",
			claims: map[string]any{
				"sub":    "user@example.com",
				"scopes": []string{"read", "write"},
			},
			want: map[string]any{
				"sub":    "user@example.com",
				"scopes": []string{"read", "write"},
			},
		},
		{
			name: "scope normalized to scopes",
			claims: map[string]any{
				"sub":   "user@example.com",
				"scope": "read write",
			},
			want: map[string]any{
				"sub":    "user@example.com",
				"scopes": "read write",
			},
		},
		{
			name: "all standard claims",
			claims: map[string]any{
				"sub":    "user@example.com",
				"roles":  []string{"admin"},
				"groups": []string{"engineering"},
				"scopes": []string{"read", "write"},
			},
			want: map[string]any{
				"sub":    "user@example.com",
				"roles":  []string{"admin"},
				"groups": []string{"engineering"},
				"scopes": []string{"read", "write"},
			},
		},
		{
			name: "ignores MPE-specific claims",
			claims: map[string]any{
				"sub":          "user@example.com",
				"mroles":       []string{"admin"},
				"mgroups":      []string{"engineering"},
				"mclearance":   "SECRET",
				"mannotations": map[string]string{"dept": "engineering"},
			},
			want: map[string]any{
				"sub": "user@example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := mapper.MapClaims(tt.claims)

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StandardClaimMapper.MapClaims() = %v, want %v", got, tt.want)
			}
		})
	}
}

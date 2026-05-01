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
			name:   testNameNilClaims,
			claims: nil,
			want:   map[string]any{},
		},
		{
			name: testNameBasicClaims,
			claims: map[string]any{
				testClaimSub: testSubjectUser,
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "mroles claim (MPE native)",
			claims: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimMroles: []string{testRoleDeveloper},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMroles:       []string{testRoleDeveloper},
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "roles mapped to mroles",
			claims: map[string]any{
				testClaimSub:   testSubjectUser,
				testClaimRoles: []string{testRoleAdmin},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMroles:       []string{testRoleAdmin},
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "mgroups claim (MPE native)",
			claims: map[string]any{
				testClaimSub:     testSubjectUser,
				testClaimMgroups: []string{testGroupEng},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMgroups:      []string{testGroupEng},
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "groups mapped to mgroups",
			claims: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimGroups: []string{testGroupEng},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMgroups:      []string{testGroupEng},
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "scopes claim",
			claims: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimScopes: []string{testScopeRead, testScopeWrite},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimScopes:       []string{testScopeRead, testScopeWrite},
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "scope mapped to scopes",
			claims: map[string]any{
				testClaimSub:   testSubjectUser,
				testClaimScope: testScopeReadWrite,
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimScopes:       testScopeReadWrite,
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "mclearance claim (MPE native)",
			claims: map[string]any{
				testClaimSub:        testSubjectUser,
				testClaimMclearance: testClearanceTop,
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMclearance:   testClearanceTop,
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "clearance mapped to mclearance",
			claims: map[string]any{
				testClaimSub: testSubjectUser,
				"clearance":  testClearanceSecret,
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMclearance:   testClearanceSecret,
				testClaimMannotations: emptyAnnotations,
			},
		},
		{
			name: "mannotations claim (MPE native)",
			claims: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMannotations: map[string]string{testDeptKey: testGroupEng},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMannotations: map[string]string{testDeptKey: testGroupEng},
			},
		},
		{
			name: "annotations mapped to mannotations",
			claims: map[string]any{
				testClaimSub:         testSubjectUser,
				testClaimAnnotations: map[string]string{testDeptKey: "sales"},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMannotations: map[string]string{testDeptKey: "sales"},
			},
		},
		{
			name: "all claims",
			claims: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMroles:       []string{testRoleAdmin},
				testClaimMgroups:      []string{testGroupEng},
				testClaimScopes:       []string{testScopeRead, testScopeWrite},
				testClaimMclearance:   testClearanceTop,
				testClaimMannotations: map[string]string{testDeptKey: testGroupEng},
			},
			want: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMroles:       []string{testRoleAdmin},
				testClaimMgroups:      []string{testGroupEng},
				testClaimScopes:       []string{testScopeRead, testScopeWrite},
				testClaimMclearance:   testClearanceTop,
				testClaimMannotations: map[string]string{testDeptKey: testGroupEng},
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
				if _, ok := got[testClaimMannotations]; !ok {
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
			name:   testNameNilClaims,
			claims: nil,
			want:   map[string]any{},
		},
		{
			name: testNameBasicClaims,
			claims: map[string]any{
				testClaimSub: testSubjectUser,
			},
			want: map[string]any{
				testClaimSub: testSubjectUser,
			},
		},
		{
			name: "roles claim",
			claims: map[string]any{
				testClaimSub:   testSubjectUser,
				testClaimRoles: []string{testRoleDeveloper},
			},
			want: map[string]any{
				testClaimSub:   testSubjectUser,
				testClaimRoles: []string{testRoleDeveloper},
			},
		},
		{
			name: "groups claim",
			claims: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimGroups: []string{testGroupEng},
			},
			want: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimGroups: []string{testGroupEng},
			},
		},
		{
			name: "scopes claim",
			claims: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimScopes: []string{testScopeRead, testScopeWrite},
			},
			want: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimScopes: []string{testScopeRead, testScopeWrite},
			},
		},
		{
			name: "scope normalized to scopes",
			claims: map[string]any{
				testClaimSub:   testSubjectUser,
				testClaimScope: testScopeReadWrite,
			},
			want: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimScopes: testScopeReadWrite,
			},
		},
		{
			name: "all standard claims",
			claims: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimRoles:  []string{testRoleAdmin},
				testClaimGroups: []string{testGroupEng},
				testClaimScopes: []string{testScopeRead, testScopeWrite},
			},
			want: map[string]any{
				testClaimSub:    testSubjectUser,
				testClaimRoles:  []string{testRoleAdmin},
				testClaimGroups: []string{testGroupEng},
				testClaimScopes: []string{testScopeRead, testScopeWrite},
			},
		},
		{
			name: "ignores MPE-specific claims",
			claims: map[string]any{
				testClaimSub:          testSubjectUser,
				testClaimMroles:       []string{testRoleAdmin},
				testClaimMgroups:      []string{testGroupEng},
				testClaimMclearance:   testClearanceSecret,
				testClaimMannotations: map[string]string{testDeptKey: testGroupEng},
			},
			want: map[string]any{
				testClaimSub: testSubjectUser,
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

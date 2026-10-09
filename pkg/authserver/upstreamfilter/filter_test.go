// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamfilter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

// configuredUpstreams used across tests. Index 0 is the mandatory first
// upstream (okta), indices 1+ are the non-first upstreams filters may select.
var testConfigured = []string{"okta", "jira", "confluence", "slack", "salesforce"}

func TestNewGroupBasedFilter_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         GroupBasedFilterConfig
		configured  []string
		wantErrSubs string // non-empty = expect error containing this substring
	}{
		{
			name:        "fewer than two configured upstreams",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}}},
			configured:  []string{"okta"},
			wantErrSubs: "at least two configured upstreams",
		},
		{
			name:        "no rules",
			cfg:         GroupBasedFilterConfig{Rules: nil},
			configured:  testConfigured,
			wantErrSubs: "at least one rule",
		},
		{
			name:        "rule with empty groups",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{}, UpstreamProviders: []string{"jira"}}}},
			configured:  testConfigured,
			wantErrSubs: "groups must not be empty",
		},
		{
			name:        "rule with empty string in groups",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"engineering", ""}, UpstreamProviders: []string{"jira"}}}},
			configured:  testConfigured,
			wantErrSubs: "groups must not contain empty values",
		},
		{
			name:        "rule with duplicate groups",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"eng", "eng"}, UpstreamProviders: []string{"jira"}}}},
			configured:  testConfigured,
			wantErrSubs: "groups must not contain duplicate values",
		},
		{
			name:        "rule with empty upstreamProviders",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{}}}},
			configured:  testConfigured,
			wantErrSubs: "upstreamProviders must not be empty",
		},
		{
			name:        "rule with empty string in upstreamProviders",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira", ""}}}},
			configured:  testConfigured,
			wantErrSubs: "must not contain empty values",
		},
		{
			name:        "rule with duplicate upstreamProviders",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira", "jira"}}}},
			configured:  testConfigured,
			wantErrSubs: "must not contain duplicate values",
		},
		{
			name:        "rule references unknown upstream",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"unknown"}}}},
			configured:  testConfigured,
			wantErrSubs: `"unknown" is not a configured upstream`,
		},
		{
			name:        "rule references mandatory first upstream",
			cfg:         GroupBasedFilterConfig{Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"okta"}}}},
			configured:  testConfigured,
			wantErrSubs: `must not reference the mandatory first upstream "okta"`,
		},
		{
			name: "defaultUpstreams references unknown upstream",
			cfg: GroupBasedFilterConfig{
				Rules:            []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}},
				DefaultUpstreams: []string{"unknown"},
			},
			configured:  testConfigured,
			wantErrSubs: `"unknown" is not a configured upstream`,
		},
		{
			name: "defaultUpstreams references first upstream",
			cfg: GroupBasedFilterConfig{
				Rules:            []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}},
				DefaultUpstreams: []string{"okta"},
			},
			configured:  testConfigured,
			wantErrSubs: `must not reference the mandatory first upstream "okta"`,
		},
		{
			name: "defaultUpstreams has duplicates",
			cfg: GroupBasedFilterConfig{
				Rules:            []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}},
				DefaultUpstreams: []string{"jira", "jira"},
			},
			configured:  testConfigured,
			wantErrSubs: "must not contain duplicate values",
		},
		{
			name: "valid config with defaults",
			cfg: GroupBasedFilterConfig{
				Claim:            "groups",
				Rules:            []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}},
				DefaultUpstreams: []string{"confluence"},
			},
			configured: testConfigured,
		},
		{
			name: "valid config without claim (defaults applied)",
			cfg: GroupBasedFilterConfig{
				Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}},
			},
			configured: testConfigured,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := NewGroupBasedFilter(tc.cfg, tc.configured)
			if tc.wantErrSubs != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrSubs)
				assert.Nil(t, f)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, f)
		})
	}
}

func TestNewGroupBasedFilter_DefaultClaimName(t *testing.T) {
	t.Parallel()

	// Empty claim string should default to "groups".
	f, err := NewGroupBasedFilter(GroupBasedFilterConfig{
		Rules: []GroupRule{{Groups: []string{"g1"}, UpstreamProviders: []string{"jira"}}},
	}, testConfigured)
	require.NoError(t, err)
	assert.Equal(t, "groups", f.claim)
}

func TestNewGroupBasedFilter_InputImmutability(t *testing.T) {
	t.Parallel()

	// Caller mutations to the config slices MUST NOT affect the constructed
	// filter — the constructor clones defensively.
	cfg := GroupBasedFilterConfig{
		Rules: []GroupRule{
			{Groups: []string{"eng"}, UpstreamProviders: []string{"jira"}},
		},
		DefaultUpstreams: []string{"confluence"},
	}
	f, err := NewGroupBasedFilter(cfg, testConfigured)
	require.NoError(t, err)

	// Mutate caller-owned slices.
	cfg.Rules[0].Groups[0] = "mutated"
	cfg.Rules[0].UpstreamProviders[0] = "mutated"
	cfg.DefaultUpstreams[0] = "mutated"

	// Filter's internal state must be unchanged.
	assert.Equal(t, "eng", f.rules[0].Groups[0])
	assert.Equal(t, "jira", f.rules[0].UpstreamProviders[0])
	assert.Equal(t, "confluence", f.defaultUpstreams[0])
}

func TestFilterUpstreams(t *testing.T) {
	t.Parallel()

	// A canonical config used across most behavior tests.
	// configured upstreams: [okta, jira, confluence, slack, salesforce]
	validCfg := GroupBasedFilterConfig{
		Claim: "groups",
		Rules: []GroupRule{
			{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira", "confluence", "slack"}},
			{Groups: []string{"sales"}, UpstreamProviders: []string{"salesforce", "slack"}},
			{Groups: []string{"admins"}, UpstreamProviders: []string{"jira", "confluence", "slack", "salesforce"}},
		},
		DefaultUpstreams: []string{"jira"},
	}

	tests := []struct {
		name    string
		claims  map[string]any
		want    []string
		wantErr bool
		errSubs string
	}{
		{
			name:    "nil claims map fails closed",
			claims:  nil,
			wantErr: true,
			errSubs: "principal claims are absent",
		},
		{
			name:    "claim not present fails closed",
			claims:  map[string]any{"email": "a@kt.com"},
			wantErr: true,
			errSubs: `claim "groups" not present`,
		},
		{
			name:    "claim value is nil fails closed",
			claims:  map[string]any{"groups": nil},
			wantErr: true,
			errSubs: "nil",
		},
		{
			name:    "claim value is wrong type (string) fails closed",
			claims:  map[string]any{"groups": "engineering"},
			wantErr: true,
			errSubs: "want JSON string array",
		},
		{
			name:    "claim value has non-string element fails closed",
			claims:  map[string]any{"groups": []any{"engineering", 42}},
			wantErr: true,
			errSubs: "want string",
		},
		{
			name:   "empty []string claim falls back to defaults",
			claims: map[string]any{"groups": []string{}},
			want:   []string{"jira"},
		},
		{
			name:   "empty []any claim falls back to defaults",
			claims: map[string]any{"groups": []any{}},
			want:   []string{"jira"},
		},
		{
			name:   "no matching rule falls back to defaults",
			claims: map[string]any{"groups": []any{"finance"}},
			want:   []string{"jira"},
		},
		{
			name:   "single rule match returns that rule's upstreams",
			claims: map[string]any{"groups": []any{"engineering"}},
			want:   []string{"jira", "confluence", "slack"},
		},
		{
			name:   "multiple rule match returns union (dedup)",
			claims: map[string]any{"groups": []any{"engineering", "sales"}},
			want:   []string{"jira", "confluence", "slack", "salesforce"},
		},
		{
			name:   "admin single rule covers all optional upstreams",
			claims: map[string]any{"groups": []any{"admins"}},
			want:   []string{"jira", "confluence", "slack", "salesforce"},
		},
		{
			name:   "typed []string claim works equivalently",
			claims: map[string]any{"groups": []string{"engineering", "sales"}},
			want:   []string{"jira", "confluence", "slack", "salesforce"},
		},
		{
			name:   "case-sensitive matching: lowercase does not match capitalized rule group",
			claims: map[string]any{"groups": []any{"Engineering"}},
			want:   []string{"jira"}, // falls back to defaults
		},
		{
			name:   "unrelated extra groups are ignored",
			claims: map[string]any{"groups": []any{"engineering", "random-group", "another"}},
			want:   []string{"jira", "confluence", "slack"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := NewGroupBasedFilter(validCfg, testConfigured)
			require.NoError(t, err)

			got, err := f.FilterUpstreams(
				context.Background(),
				auth.PrincipalInfo{Claims: tc.claims},
				testConfigured[1:],
			)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errSubs != "" {
					assert.Contains(t, err.Error(), tc.errSubs)
				}
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFilterUpstreams_CustomClaimName(t *testing.T) {
	t.Parallel()

	cfg := GroupBasedFilterConfig{
		Claim: "cognito:groups",
		Rules: []GroupRule{
			{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira"}},
		},
	}
	f, err := NewGroupBasedFilter(cfg, testConfigured)
	require.NoError(t, err)

	got, err := f.FilterUpstreams(
		context.Background(),
		auth.PrincipalInfo{Claims: map[string]any{"cognito:groups": []any{"engineering"}}},
		testConfigured[1:],
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"jira"}, got)
}

func TestFilterUpstreams_EmptyDefaultsReturnsEmpty(t *testing.T) {
	t.Parallel()

	// When DefaultUpstreams is omitted and no rule matches, FilterUpstreams
	// should return an empty slice (so only the mandatory first upstream remains).
	cfg := GroupBasedFilterConfig{
		Rules: []GroupRule{
			{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira"}},
		},
	}
	f, err := NewGroupBasedFilter(cfg, testConfigured)
	require.NoError(t, err)

	got, err := f.FilterUpstreams(
		context.Background(),
		auth.PrincipalInfo{Claims: map[string]any{"groups": []any{"finance"}}},
		testConfigured[1:],
	)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFilterUpstreams_DoesNotMutatePrincipalClaims(t *testing.T) {
	t.Parallel()

	// The filter contract requires principal (including Claims) to be
	// read-only. Verify we don't mutate the claim slice.
	cfg := GroupBasedFilterConfig{
		Rules: []GroupRule{
			{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira"}},
		},
	}
	f, err := NewGroupBasedFilter(cfg, testConfigured)
	require.NoError(t, err)

	originalGroups := []any{"engineering", "sales"}
	claims := map[string]any{"groups": originalGroups}

	_, err = f.FilterUpstreams(context.Background(), auth.PrincipalInfo{Claims: claims}, testConfigured[1:])
	require.NoError(t, err)

	// The underlying slice must be unchanged.
	assert.Equal(t, []any{"engineering", "sales"}, originalGroups)
}

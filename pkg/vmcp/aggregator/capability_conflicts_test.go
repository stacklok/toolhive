// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// These tests are deliberately adversarial: the bug they pin (#6060) survived
// because every earlier fixture minted strictly unique keys and therefore
// could not express a collision. Each case here makes at least two backends
// share an identity.

func TestDefaultAggregator_ResolveConflicts_ResourceAndTemplateDedup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		capabilities map[string]*BackendCapabilities
		// wantResources maps advertised URI -> owning backend; the advertised
		// list must contain exactly these URIs, sorted.
		wantResources map[string]string
		// wantTemplates maps advertised URI template -> owning backend.
		wantTemplates map[string]string
	}{
		{
			name: "two backends share a URI, first sorted backend wins",
			capabilities: map[string]*BackendCapabilities{
				"beta": {
					BackendID: "beta",
					Resources: []vmcp.Resource{newTestResource("file:///README.md", "beta")},
				},
				"alpha": {
					BackendID: "alpha",
					Resources: []vmcp.Resource{
						newTestResource("file:///README.md", "alpha"),
						newTestResource("res://only-alpha", "alpha"),
					},
				},
			},
			wantResources: map[string]string{
				"file:///README.md": "alpha",
				"res://only-alpha":  "alpha",
			},
		},
		{
			name: "three backends share one URI",
			capabilities: map[string]*BackendCapabilities{
				"b3": {BackendID: "b3", Resources: []vmcp.Resource{newTestResource("res://shared", "b3")}},
				"b1": {BackendID: "b1", Resources: []vmcp.Resource{newTestResource("res://shared", "b1")}},
				"b2": {BackendID: "b2", Resources: []vmcp.Resource{newTestResource("res://shared", "b2")}},
			},
			wantResources: map[string]string{"res://shared": "b1"},
		},
		{
			name: "same backend advertises a URI twice, first occurrence wins",
			capabilities: map[string]*BackendCapabilities{
				"solo": {
					BackendID: "solo",
					Resources: []vmcp.Resource{
						{URI: "res://twice", Name: "first", BackendID: "solo"},
						{URI: "res://twice", Name: "second", BackendID: "solo"},
					},
				},
			},
			wantResources: map[string]string{"res://twice": "solo"},
		},
		{
			name: "two backends share a resource template string",
			capabilities: map[string]*BackendCapabilities{
				"b2": {
					BackendID: "b2",
					ResourceTemplates: []vmcp.ResourceTemplate{
						{URITemplate: "file:///logs/{date}.txt", Name: "b2 logs", BackendID: "b2"},
					},
				},
				"b1": {
					BackendID: "b1",
					ResourceTemplates: []vmcp.ResourceTemplate{
						{URITemplate: "file:///logs/{date}.txt", Name: "b1 logs", BackendID: "b1"},
						{URITemplate: "file:///cfg/{name}.yaml", Name: "b1 cfg", BackendID: "b1"},
					},
				},
			},
			wantTemplates: map[string]string{
				"file:///logs/{date}.txt": "b1",
				"file:///cfg/{name}.yaml": "b1",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			agg := NewDefaultAggregator(nil, nil, nil, nil)

			// Repeat: map iteration order is re-randomized per call, and the
			// dedup winner must not depend on it.
			for range 5 {
				resolved, err := agg.ResolveConflicts(context.Background(), tt.capabilities)
				require.NoError(t, err)

				gotResources := make(map[string]string, len(resolved.Resources))
				var lastURI string
				for _, res := range resolved.Resources {
					_, dup := gotResources[res.URI]
					assert.Falsef(t, dup, "URI %q advertised more than once", res.URI)
					assert.LessOrEqual(t, lastURI, res.URI, "resources must be sorted by URI")
					lastURI = res.URI
					gotResources[res.URI] = res.BackendID
				}
				if tt.wantResources != nil {
					assert.Equal(t, tt.wantResources, gotResources)
				}

				gotTemplates := make(map[string]string, len(resolved.ResourceTemplates))
				for _, tmpl := range resolved.ResourceTemplates {
					_, dup := gotTemplates[tmpl.URITemplate]
					assert.Falsef(t, dup, "template %q advertised more than once", tmpl.URITemplate)
					gotTemplates[tmpl.URITemplate] = tmpl.BackendID
				}
				if tt.wantTemplates != nil {
					assert.Equal(t, tt.wantTemplates, gotTemplates)
				}
			}
		})
	}
}

func TestDefaultAggregator_ResolveConflicts_PromptConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		capabilities map[string]*BackendCapabilities
		// want maps advertised prompt name -> {owning backend, original name}.
		want map[string][2]string
		// wantAbsent lists names that must NOT be advertised.
		wantAbsent []string
	}{
		{
			// Prefixing is unconditional: the advertised name is a pure
			// function of (backendID, name), so it cannot shift under a
			// client (or a Cedar policy) when unrelated backends join.
			name: "unique prompt names are prefixed too",
			capabilities: map[string]*BackendCapabilities{
				"b1": {BackendID: "b1", Prompts: []vmcp.Prompt{newTestPrompt("review", "b1")}},
				"b2": {BackendID: "b2", Prompts: []vmcp.Prompt{newTestPrompt("summarize", "b2")}},
			},
			want: map[string][2]string{
				"b1_review":    {"b1", "review"},
				"b2_summarize": {"b2", "summarize"},
			},
			wantAbsent: []string{"review", "summarize"},
		},
		{
			name: "name shared by two backends stays unambiguous",
			capabilities: map[string]*BackendCapabilities{
				"b2": {BackendID: "b2", Prompts: []vmcp.Prompt{newTestPrompt("review", "b2")}},
				"b1": {
					BackendID: "b1",
					Prompts:   []vmcp.Prompt{newTestPrompt("review", "b1"), newTestPrompt("unique", "b1")},
				},
			},
			want: map[string][2]string{
				"b1_review": {"b1", "review"},
				"b2_review": {"b2", "review"},
				"b1_unique": {"b1", "unique"},
			},
			wantAbsent: []string{"review", "unique"},
		},
		{
			// Under collision-only renaming this shape dropped a prompt: b2's
			// literal "b1_review" collided with b1's renamed "review".
			// Unconditional prefixing keeps all three reachable.
			name: "literal prefixed name from another backend no longer collides",
			capabilities: map[string]*BackendCapabilities{
				"b1": {BackendID: "b1", Prompts: []vmcp.Prompt{newTestPrompt("review", "b1")}},
				"b2": {BackendID: "b2", Prompts: []vmcp.Prompt{newTestPrompt("b1_review", "b2")}},
				"b3": {BackendID: "b3", Prompts: []vmcp.Prompt{newTestPrompt("review", "b3")}},
			},
			want: map[string][2]string{
				"b1_review":    {"b1", "review"},
				"b2_b1_review": {"b2", "b1_review"},
				"b3_review":    {"b3", "review"},
			},
			wantAbsent: []string{"review"},
		},
		{
			// An exact intra-backend duplicate advertises and routes
			// identically, so the later occurrence is dropped, not an error.
			name: "same backend advertises a prompt name twice",
			capabilities: map[string]*BackendCapabilities{
				"solo": {
					BackendID: "solo",
					Prompts:   []vmcp.Prompt{newTestPrompt("dup", "solo"), newTestPrompt("dup", "solo")},
				},
			},
			want:       map[string][2]string{"solo_dup": {"solo", "dup"}},
			wantAbsent: []string{"dup"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			agg := NewDefaultAggregator(nil, nil, nil, nil)

			for range 5 {
				resolved, err := agg.ResolveConflicts(context.Background(), tt.capabilities)
				require.NoError(t, err)

				got := make(map[string][2]string, len(resolved.Prompts))
				var lastName string
				for _, prompt := range resolved.Prompts {
					_, dup := got[prompt.Name]
					assert.Falsef(t, dup, "prompt %q advertised more than once", prompt.Name)
					assert.LessOrEqual(t, lastName, prompt.Name, "prompts must be sorted by resolved name")
					lastName = prompt.Name
					got[prompt.Name] = [2]string{prompt.BackendID, prompt.OriginalName}
				}
				assert.Equal(t, tt.want, got)
				for _, absent := range tt.wantAbsent {
					assert.NotContains(t, got, absent)
				}
			}
		})
	}
}

// TestDefaultAggregator_ResolveConflicts_PromptPrefixAmbiguity pins the one
// residual collision unconditional prefixing cannot rule out: two distinct
// (backendID, name) pairs composing to the same prefixed string. That name
// would be ambiguous between backends, so aggregation must fail loudly
// instead of silently dropping one of the prompts.
func TestDefaultAggregator_ResolveConflicts_PromptPrefixAmbiguity(t *testing.T) {
	t.Parallel()

	capabilities := map[string]*BackendCapabilities{
		// "b1" + "x_y" and "b1_x" + "y" both advertise as "b1_x_y".
		"b1":   {BackendID: "b1", Prompts: []vmcp.Prompt{newTestPrompt("x_y", "b1")}},
		"b1_x": {BackendID: "b1_x", Prompts: []vmcp.Prompt{newTestPrompt("y", "b1_x")}},
	}

	agg := NewDefaultAggregator(nil, nil, nil, nil)
	resolved, err := agg.ResolveConflicts(context.Background(), capabilities)
	require.ErrorIs(t, err, ErrUnresolvedConflicts)
	assert.Contains(t, err.Error(), "b1_x_y")
	assert.Nil(t, resolved)
}

// TestDefaultAggregator_ResolveConflicts_PromptPrefixFormat verifies prompt
// renaming honours conflictResolutionConfig.prefixFormat — the same knob the
// tool prefix strategy uses — rather than a hardcoded "_" separator.
func TestDefaultAggregator_ResolveConflicts_PromptPrefixFormat(t *testing.T) {
	t.Parallel()

	capabilities := map[string]*BackendCapabilities{
		"github": {BackendID: "github", Prompts: []vmcp.Prompt{newTestPrompt("review", "github")}},
	}
	aggCfg := &config.AggregationConfig{
		ConflictResolutionConfig: &config.ConflictResolutionConfig{PrefixFormat: "{workload}."},
	}

	agg := NewDefaultAggregator(nil, nil, aggCfg, nil)
	resolved, err := agg.ResolveConflicts(context.Background(), capabilities)
	require.NoError(t, err)

	require.Len(t, resolved.Prompts, 1)
	assert.Equal(t, "github.review", resolved.Prompts[0].Name)
	assert.Equal(t, "review", resolved.Prompts[0].OriginalName)
}

// TestDefaultAggregator_AggregateCapabilities_CollisionRouting exercises the
// full pipeline with colliding identities across two backends and asserts the
// client-visible outcome: one advertised entry per resource URI routed to a
// deterministic backend, and both prompts advertised under their (always
// prefixed) names whose routing translates back to the backend's own prompt
// name.
func TestDefaultAggregator_AggregateCapabilities_CollisionRouting(t *testing.T) {
	t.Parallel()

	backends := []vmcp.Backend{
		newTestBackend("backend1", withBackendName("Backend 1")),
		newTestBackend("backend2", withBackendName("Backend 2")),
	}
	capsByID := map[string]*vmcp.CapabilityList{
		"backend1": newTestCapabilityList(
			withResources(newTestResource("file:///README.md", "backend1")),
			withPrompts(newTestPrompt("review", "backend1")),
		),
		"backend2": newTestCapabilityList(
			withResources(newTestResource("file:///README.md", "backend2")),
			withPrompts(newTestPrompt("review", "backend2")),
		),
	}

	// Aggregate repeatedly: the collision winner must be stable across runs,
	// not whichever backend won a map write.
	for run := range 5 {
		ctrl := gomock.NewController(t)
		mockClient := mocks.NewMockBackendClient(ctrl)
		expectListCapabilities(mockClient, capsByID)

		agg := NewDefaultAggregator(mockClient, nil, nil, nil)
		result, err := agg.AggregateCapabilities(context.Background(), backends)
		require.NoError(t, err)

		// The duplicated URI is advertised once and reads route to backend1
		// (first in sorted backend order) -- a defined, stable outcome.
		require.Lenf(t, result.Resources, 1, "run %d", run)
		assert.Equal(t, "file:///README.md", result.Resources[0].URI)
		assert.Equal(t, "backend1", result.Resources[0].BackendID)
		target := result.RoutingTable.Resources["file:///README.md"]
		require.NotNil(t, target)
		assert.Equal(t, "backend1", target.WorkloadID)

		// Both colliding prompts survive under prefixed names; the bare name is
		// neither advertised nor routable; prompts/get on a prefixed name
		// forwards the backend's own prompt name.
		require.Lenf(t, result.Prompts, 2, "run %d", run)
		assert.Equal(t, "backend1_review", result.Prompts[0].Name)
		assert.Equal(t, "backend2_review", result.Prompts[1].Name)
		assert.NotContains(t, result.RoutingTable.Prompts, "review")
		for _, advertised := range []string{"backend1_review", "backend2_review"} {
			promptTarget := result.RoutingTable.Prompts[advertised]
			require.NotNilf(t, promptTarget, "routing target for %q", advertised)
			assert.Equal(t, "review", promptTarget.GetBackendCapabilityName(advertised),
				"prompts/get on %q must forward the backend's own name", advertised)
		}

		assert.Equal(t, 1, result.Metadata.ResourceCount)
		assert.Equal(t, 2, result.Metadata.PromptCount)
		ctrl.Finish()
	}
}

// TestDefaultAggregator_ResolveConflicts_DuplicateAtPageBoundary places the
// collision at the Modern paginator's page boundary (page size 1000): the exact
// shape that permanently dropped items before the paginator was made
// collision-safe. The aggregator must now hand the paginator a corpus with no
// duplicate keys at all, boundary included.
func TestDefaultAggregator_ResolveConflicts_DuplicateAtPageBoundary(t *testing.T) {
	t.Parallel()

	const total = 1100
	const pageBoundary = 1000 // pkg/vmcp/server modernPageSize

	// backend-a advertises 1100 resources whose URIs sort to their index.
	// backend-b re-advertises the URIs at sorted positions 999 and 1000, so the
	// duplicates straddle the page-1/page-2 boundary.
	resourcesA := make([]vmcp.Resource, 0, total)
	for i := range total {
		resourcesA = append(resourcesA, newTestResource(fmt.Sprintf("res://%05d", i), "backend-a"))
	}
	capabilities := map[string]*BackendCapabilities{
		"backend-a": {BackendID: "backend-a", Resources: resourcesA},
		"backend-b": {BackendID: "backend-b", Resources: []vmcp.Resource{
			newTestResource(fmt.Sprintf("res://%05d", pageBoundary-1), "backend-b"),
			newTestResource(fmt.Sprintf("res://%05d", pageBoundary), "backend-b"),
		}},
	}

	agg := NewDefaultAggregator(nil, nil, nil, nil)
	resolved, err := agg.ResolveConflicts(context.Background(), capabilities)
	require.NoError(t, err)

	require.Len(t, resolved.Resources, total, "duplicates must collapse, nothing else may be lost")
	for i, res := range resolved.Resources {
		assert.Equal(t, fmt.Sprintf("res://%05d", i), res.URI, "output must be sorted and duplicate-free")
		assert.Equal(t, "backend-a", res.BackendID, "backend-a sorts first and owns every duplicated URI")
	}
}

// TestDefaultAggregator_MergeCapabilities_FirstWinsOnDuplicates pins the
// defence-in-depth guard: MergeCapabilities is independently callable, and if
// it is handed unresolved duplicates anyway, the first entry wins in BOTH the
// routing table and the advertised list -- never a silent map overwrite that
// leaves advertising and routing disagreeing.
func TestDefaultAggregator_MergeCapabilities_FirstWinsOnDuplicates(t *testing.T) {
	t.Parallel()

	resolved := &ResolvedCapabilities{
		Tools: map[string]*ResolvedTool{},
		Resources: []vmcp.Resource{
			newTestResource("res://dup", "backend1"),
			newTestResource("res://dup", "backend2"),
		},
		ResourceTemplates: []vmcp.ResourceTemplate{
			{URITemplate: "res://tmpl/{id}", Name: "first", BackendID: "backend1"},
			{URITemplate: "res://tmpl/{id}", Name: "second", BackendID: "backend2"},
		},
		Prompts: []ResolvedPrompt{
			{Prompt: vmcp.Prompt{Name: "dup_prompt", BackendID: "backend1"}, OriginalName: "dup_prompt"},
			{Prompt: vmcp.Prompt{Name: "dup_prompt", BackendID: "backend2"}, OriginalName: "dup_prompt"},
		},
	}
	registry := vmcp.NewImmutableRegistry([]vmcp.Backend{
		newTestBackend("backend1"),
		newTestBackend("backend2"),
	})

	agg := NewDefaultAggregator(nil, nil, nil, nil)
	aggregated, err := agg.MergeCapabilities(context.Background(), resolved, registry)
	require.NoError(t, err)

	require.Len(t, aggregated.Resources, 1)
	assert.Equal(t, "backend1", aggregated.Resources[0].BackendID)
	assert.Equal(t, "backend1", aggregated.RoutingTable.Resources["res://dup"].WorkloadID)

	require.Len(t, aggregated.ResourceTemplates, 1)
	assert.Equal(t, "backend1", aggregated.ResourceTemplates[0].BackendID)
	assert.Equal(t, "backend1", aggregated.RoutingTable.ResourceTemplates["res://tmpl/{id}"].WorkloadID)

	require.Len(t, aggregated.Prompts, 1)
	assert.Equal(t, "backend1", aggregated.Prompts[0].BackendID)
	assert.Equal(t, "backend1", aggregated.RoutingTable.Prompts["dup_prompt"].WorkloadID)

	assert.Equal(t, 1, aggregated.Metadata.ResourceCount)
	assert.Equal(t, 1, aggregated.Metadata.ResourceTemplateCount)
	assert.Equal(t, 1, aggregated.Metadata.PromptCount)
}

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

// This file pins the BackendID population contract committed to by RFC THV-0076:
// every advertised Tool, Resource, and Prompt must carry a non-empty BackendID,
// across all conflict-resolution strategies and advertising-filter modes. BackendID
// is the logical, safe-to-expose backend identifier that decorators see and that
// LookupTool/CallTool rely on to route renamed/prefixed names back to their backend.
//
// These are contract tests: production code already populates BackendID at every
// resolver and merge site, and the backend client populates it on resources/prompts.
// The assertions below guard against a future regression silently shipping an empty
// BackendID, which would break logical-backend routing and decorator visibility.

// assertAdvertisedBackendIDs asserts the BackendID invariant on every advertised
// capability returned by the aggregator.
func assertAdvertisedBackendIDs(t *testing.T, result *AggregatedCapabilities) {
	t.Helper()
	for _, tool := range result.Tools {
		assert.NotEmptyf(t, tool.BackendID,
			"advertised tool %q must carry a non-empty BackendID", tool.Name)
	}
	for _, res := range result.Resources {
		assert.NotEmptyf(t, res.BackendID,
			"advertised resource %q must carry a non-empty BackendID", res.URI)
	}
	for _, prompt := range result.Prompts {
		assert.NotEmptyf(t, prompt.BackendID,
			"advertised prompt %q must carry a non-empty BackendID", prompt.Name)
	}
}

// resolverForStrategy builds the conflict resolver matching the given strategy.
// For the manual strategy, the same workload configs that drive the per-backend
// overrides are fed to the resolver constructor.
func resolverForStrategy(
	t *testing.T,
	strategy vmcp.ConflictResolutionStrategy,
	priorityOrder []string,
	wlConfigs []*config.WorkloadToolConfig,
) ConflictResolver {
	t.Helper()
	switch strategy {
	case vmcp.ConflictStrategyPrefix:
		return NewPrefixConflictResolver("{workload}_")
	case vmcp.ConflictStrategyPriority:
		r, err := NewPriorityConflictResolver(priorityOrder)
		require.NoError(t, err)
		return r
	case vmcp.ConflictStrategyManual:
		r, err := NewManualConflictResolver(wlConfigs)
		require.NoError(t, err)
		return r
	default:
		t.Fatalf("unknown conflict strategy %q", strategy)
		return nil
	}
}

// twoBackendCaps returns two backends that share a conflicting tool ("fetch"),
// each with a unique tool plus a resource and a prompt. This forces conflict
// resolution to rename/prefix/drop while exercising resources and prompts.
func twoBackendCaps() ([]vmcp.Backend, map[string]*vmcp.CapabilityList) {
	backends := []vmcp.Backend{
		newTestBackend("backend1", withBackendName("Backend 1")),
		newTestBackend("backend2", withBackendName("Backend 2")),
	}
	capsByID := map[string]*vmcp.CapabilityList{
		"backend1": newTestCapabilityList(
			withTools(newTestTool("fetch", "backend1"), newTestTool("tool_a", "backend1")),
			withResources(newTestResource("res://a", "backend1")),
			withPrompts(newTestPrompt("prompt_a", "backend1")),
		),
		"backend2": newTestCapabilityList(
			withTools(newTestTool("fetch", "backend2"), newTestTool("tool_b", "backend2")),
			withResources(newTestResource("res://b", "backend2")),
			withPrompts(newTestPrompt("prompt_b", "backend2")),
		),
	}
	return backends, capsByID
}

// expectListCapabilities wires the mock to return per-backend capabilities keyed
// by the queried backend ID (QueryAllCapabilities runs queries in parallel, so the
// call order is non-deterministic).
func expectListCapabilities(mockClient *mocks.MockBackendClient, capsByID map[string]*vmcp.CapabilityList) {
	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			caps, ok := capsByID[target.WorkloadID]
			if !ok {
				return nil, fmt.Errorf("unexpected backend %q", target.WorkloadID)
			}
			return caps, nil
		}).Times(len(capsByID))
}

func findAdvertisedTool(tools []vmcp.Tool, name string) (vmcp.Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return vmcp.Tool{}, false
}

// renameExpectation describes a renamed/prefixed advertised tool and how its
// advertised name must resolve back to the backend's original capability name.
type renameExpectation struct {
	backendID             string // expected BackendID on the advertised tool + routing target
	backendCapabilityName string // expected GetBackendCapabilityName(advertisedName)
}

func TestDefaultAggregator_AdvertisedCapabilitiesCarryBackendID(t *testing.T) {
	t.Parallel()

	// Manual strategy resolves the "fetch" conflict via per-backend overrides
	// applied before conflict resolution; the same configs drive the resolver.
	manualConfigs := []*config.WorkloadToolConfig{
		{Workload: "backend1", Overrides: map[string]*config.ToolOverride{"fetch": {Name: "custom_fetch_b1"}}},
		{Workload: "backend2", Overrides: map[string]*config.ToolOverride{"fetch": {Name: "custom_fetch_b2"}}},
	}

	tests := []struct {
		name          string
		strategy      vmcp.ConflictResolutionStrategy
		priorityOrder []string
		wlConfigs     []*config.WorkloadToolConfig
		// renameChecks asserts the renamed/prefixed routing path used by
		// LookupTool/CallTool: advertised name -> backend's original name.
		renameChecks map[string]renameExpectation
	}{
		{
			name:     "prefix strategy prefixes conflicting and unique tools",
			strategy: vmcp.ConflictStrategyPrefix,
			renameChecks: map[string]renameExpectation{
				"backend1_fetch": {backendID: "backend1", backendCapabilityName: "fetch"},
				"backend2_fetch": {backendID: "backend2", backendCapabilityName: "fetch"},
			},
		},
		{
			name:          "priority strategy keeps the highest-priority backend",
			strategy:      vmcp.ConflictStrategyPriority,
			priorityOrder: []string{"backend1", "backend2"},
			// Priority does not rename; "fetch" resolves to itself on backend1.
			renameChecks: map[string]renameExpectation{
				"fetch": {backendID: "backend1", backendCapabilityName: "fetch"},
			},
		},
		{
			name:      "manual strategy renames conflicts via overrides",
			strategy:  vmcp.ConflictStrategyManual,
			wlConfigs: manualConfigs,
			renameChecks: map[string]renameExpectation{
				"custom_fetch_b1": {backendID: "backend1", backendCapabilityName: "fetch"},
				"custom_fetch_b2": {backendID: "backend2", backendCapabilityName: "fetch"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			backends, capsByID := twoBackendCaps()
			mockClient := mocks.NewMockBackendClient(ctrl)
			expectListCapabilities(mockClient, capsByID)

			// aggregationConfig carries the manual overrides so that
			// processBackendTools renames tools and actualBackendCapabilityName
			// can reverse the rename for routing. nil for prefix/priority.
			var aggCfg *config.AggregationConfig
			if len(tt.wlConfigs) > 0 {
				aggCfg = &config.AggregationConfig{Tools: tt.wlConfigs}
			}
			resolver := resolverForStrategy(t, tt.strategy, tt.priorityOrder, tt.wlConfigs)

			agg := NewDefaultAggregator(mockClient, resolver, aggCfg, nil)
			result, err := agg.AggregateCapabilities(context.Background(), backends)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Core invariant: every advertised capability carries a BackendID.
			assertAdvertisedBackendIDs(t, result)

			// Resources and prompts are never filtered, so all are advertised.
			require.Len(t, result.Resources, 2)
			require.Len(t, result.Prompts, 2)

			// Renamed/prefixed names must resolve back to the backend's original
			// capability name, and the advertised tool's BackendID must agree with
			// the routing target's backend.
			for advertisedName, want := range tt.renameChecks {
				tool, ok := findAdvertisedTool(result.Tools, advertisedName)
				require.Truef(t, ok, "expected advertised tool %q", advertisedName)
				assert.Equal(t, want.backendID, tool.BackendID,
					"advertised tool %q BackendID", advertisedName)

				target := result.RoutingTable.Tools[advertisedName]
				require.NotNilf(t, target, "routing target for %q", advertisedName)
				assert.Equal(t, want.backendID, target.WorkloadID,
					"routing target %q WorkloadID", advertisedName)
				assert.Equal(t, want.backendCapabilityName, target.GetBackendCapabilityName(advertisedName),
					"GetBackendCapabilityName(%q) must resolve to the backend's original name", advertisedName)
			}
		})
	}
}

func TestDefaultAggregator_AdvertisingFilterPreservesBackendID(t *testing.T) {
	t.Parallel()

	// Single backend with two tools plus a resource and prompt. Prefix strategy
	// is used so advertised tool names are deterministic ("be_<tool>").
	backendID := "be"
	newCaps := func() map[string]*vmcp.CapabilityList {
		return map[string]*vmcp.CapabilityList{
			backendID: newTestCapabilityList(
				withTools(newTestTool("tool_a", backendID), newTestTool("tool_b", backendID)),
				withResources(newTestResource("res://x", backendID)),
				withPrompts(newTestPrompt("prompt_x", backendID)),
			),
		}
	}

	tests := []struct {
		name           string
		aggCfg         *config.AggregationConfig
		wantAdvertised []string // resolved (prefixed) advertised tool names
	}{
		{
			name:           "global ExcludeAllTools hides every tool",
			aggCfg:         &config.AggregationConfig{ExcludeAllTools: true},
			wantAdvertised: nil,
		},
		{
			name: "per-workload ExcludeAll hides the workload's tools",
			aggCfg: &config.AggregationConfig{
				Tools: []*config.WorkloadToolConfig{{Workload: backendID, ExcludeAll: true}},
			},
			wantAdvertised: nil,
		},
		{
			name: "per-workload Filter advertises only matching tools",
			aggCfg: &config.AggregationConfig{
				Tools: []*config.WorkloadToolConfig{{Workload: backendID, Filter: []string{"tool_a"}}},
			},
			wantAdvertised: []string{"be_tool_a"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			capsByID := newCaps()
			mockClient := mocks.NewMockBackendClient(ctrl)
			expectListCapabilities(mockClient, capsByID)

			agg := NewDefaultAggregator(mockClient, NewPrefixConflictResolver("{workload}_"), tt.aggCfg, nil)
			result, err := agg.AggregateCapabilities(context.Background(), []vmcp.Backend{
				newTestBackend(backendID, withBackendName("Backend")),
			})
			require.NoError(t, err)
			require.NotNil(t, result)

			// Every advertised capability (the post-filter subset) carries a BackendID.
			assertAdvertisedBackendIDs(t, result)

			advertisedNames := make([]string, 0, len(result.Tools))
			for _, tool := range result.Tools {
				advertisedNames = append(advertisedNames, tool.Name)
			}
			assert.ElementsMatch(t, tt.wantAdvertised, advertisedNames,
				"advertised tool set must match the filter mode")

			// The advertising filter affects advertising only, not routing: every
			// tool stays in the routing table with its backend identity intact.
			require.NotNil(t, result.RoutingTable)
			assert.Len(t, result.RoutingTable.Tools, 2,
				"routing table must retain all tools regardless of advertising filter")
			for name, target := range result.RoutingTable.Tools {
				assert.Equalf(t, backendID, target.WorkloadID,
					"routing target %q must retain its backend identity", name)
			}

			// Resources and prompts are not gated by the advertising filter.
			require.Len(t, result.Resources, 1)
			require.Len(t, result.Prompts, 1)
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// twoBackendView is an aggregated view of b1 and b2 with one item of every
// kind on each, plus a composite-like tool with no backend.
func twoBackendView() *aggregator.AggregatedCapabilities {
	return &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			toolOn("t1", "b1"), toolOn("t1_denied", "b1"), toolOn("t2", "b2"), {Name: "backendless"},
		},
		Resources: []vmcp.Resource{
			{URI: "res://b1", BackendID: "b1"}, {URI: "res://b2", BackendID: "b2"},
		},
		ResourceTemplates: []vmcp.ResourceTemplate{
			{URITemplate: "tmpl://b1/{x}", BackendID: "b1"}, {URITemplate: "tmpl://b2/{x}", BackendID: "b2"},
		},
		Prompts: []vmcp.Prompt{
			{Name: "p1", BackendID: "b1"}, {Name: "p2", BackendID: "b2"},
		},
	}
}

// newBackendCapabilitiesCore builds a core over backends whose admission allows
// only the named items, and returns it with the authorizer that records every
// admission decision.
func newBackendCapabilitiesCore(
	t *testing.T, backends []vmcp.Backend, allowed ...string,
) (*coreVMCP, *coreMocks, *mockAuthorizer) {
	t.Helper()
	cfg, m := baseConfig(t)
	m.reg.EXPECT().List(gomock.Any()).Return(backends).AnyTimes()
	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	results := make(map[string]mockResult, len(allowed))
	for _, id := range allowed {
		results[id] = mockResult{authorized: true}
	}
	authorizer := &mockAuthorizer{results: results}
	core := c.(*coreVMCP)
	core.admission = newCedarAdmission(authorizer)
	return core, m, authorizer
}

// TestLookupBackendCapabilities_OneAggregation proves the backend's items come
// from one aggregation of the backends the lists include, filtered to that
// backend and to what identity is admitted to, and that only that backend's
// items are run through admission.
func TestLookupBackendCapabilities_OneAggregation(t *testing.T) {
	t.Parallel()
	backends := []vmcp.Backend{
		{ID: "b1", HealthStatus: vmcp.BackendHealthy},
		{ID: "b2", HealthStatus: vmcp.BackendDegraded},
		{ID: "b3", HealthStatus: vmcp.BackendUnhealthy},
	}
	c, m, authorizer := newBackendCapabilitiesCore(t, backends,
		"t1", "t2", "res://b1", "tmpl://b1/{x}", "p1")
	view := twoBackendView()
	view.UnavailableLists = map[string][]string{"b1": {"prompts/list"}, "b2": {"resources/list"}}
	var aggregated []vmcp.Backend
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			aggregated = backends
			return view, nil
		}).Times(1)

	caps, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b1")
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2"}, backendIDs(aggregated),
		"the backends the lists include, health-filtered as the lists are")
	assert.Equal(t, []vmcp.Tool{toolOn("t1", "b1")}, caps.Tools)
	assert.Equal(t, []vmcp.Resource{{URI: "res://b1", BackendID: "b1"}}, caps.Resources)
	assert.Equal(t, []vmcp.ResourceTemplate{{URITemplate: "tmpl://b1/{x}", BackendID: "b1"}}, caps.ResourceTemplates)
	assert.Equal(t, []vmcp.Prompt{{Name: "p1", BackendID: "b1"}}, caps.Prompts)
	assert.Equal(t, []string{"prompts/list"}, caps.UnavailableLists)

	var decided []string
	for _, call := range authorizer.calls {
		decided = append(decided, call.resourceID)
	}
	assert.ElementsMatch(t, []string{"t1", "t1_denied", "res://b1", "tmpl://b1/{x}", "p1"}, decided,
		"admission runs over the backend's own items only")
}

// TestLookupBackendCapabilities_NotFound proves that an unknown backend, one
// that offers identity nothing, one with no items, and one whose discovery
// fails all get vmcp.ErrNotFound, and nothing else.
func TestLookupBackendCapabilities_NotFound(t *testing.T) {
	t.Parallel()
	failed := fmt.Errorf("failed to query backends: %w", aggregator.ErrNoBackendCapabilities)
	for _, tc := range []struct {
		name      string
		backendID string
		view      *aggregator.AggregatedCapabilities
		aggErr    error
		allowed   []string
	}{
		{name: "unknown", backendID: "missing"},
		{name: "empty id", backendID: ""},
		{name: "nothing admitted", backendID: "b1", view: twoBackendView(), allowed: []string{"t2"}},
		{name: "failed discovery", backendID: "b1", view: &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{toolOn("t2", "b2")},
		}, allowed: []string{"t1", "t2"}},
		{name: "every backend failed discovery", backendID: "b1", aggErr: failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{{ID: "b1"}, {ID: "b2"}}, tc.allowed...)
			if tc.view != nil || tc.aggErr != nil {
				m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(tc.view, tc.aggErr)
			}
			_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), tc.backendID)
			require.ErrorIs(t, err, vmcp.ErrNotFound)
			assert.NotErrorIs(t, err, vmcp.ErrBackendUnavailable)
		})
	}
}

// TestLookupBackendCapabilities_AggregationError proves an aggregation failure
// other than every backend failing discovery is returned as it is, not as
// vmcp.ErrNotFound.
func TestLookupBackendCapabilities_AggregationError(t *testing.T) {
	t.Parallel()
	c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{{ID: "b1"}}, "t1")
	boom := errors.New("boom")
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, boom)
	_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b1")
	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, vmcp.ErrNotFound)
}

// TestLookupBackendCapabilities_LeftOutForHealth proves a backend the lists
// leave out for its health status is aggregated alone: in identity's view, by
// any kind of item including resource templates, it gets
// vmcp.ErrBackendUnavailable; out of it, or unreachable, vmcp.ErrNotFound.
func TestLookupBackendCapabilities_LeftOutForHealth(t *testing.T) {
	t.Parallel()
	templatesOnly := &aggregator.AggregatedCapabilities{
		ResourceTemplates: []vmcp.ResourceTemplate{{URITemplate: "tmpl://b2/{x}", BackendID: "b2"}},
	}
	for _, tc := range []struct {
		name    string
		view    *aggregator.AggregatedCapabilities
		aggErr  error
		allowed []string
		want    error
	}{
		{name: "templates only, admitted", view: templatesOnly, allowed: []string{"tmpl://b2/{x}"},
			want: vmcp.ErrBackendUnavailable},
		{name: "nothing admitted", view: templatesOnly, want: vmcp.ErrNotFound},
		{name: "unreachable", aggErr: fmt.Errorf("x: %w", aggregator.ErrNoBackendCapabilities), want: vmcp.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{
				{ID: "b1", HealthStatus: vmcp.BackendHealthy},
				{ID: "b2", HealthStatus: vmcp.BackendUnhealthy},
			}, tc.allowed...)
			var aggregated []vmcp.Backend
			m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
					aggregated = backends
					return tc.view, tc.aggErr
				}).Times(1)
			_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b2")
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, []string{"b2"}, backendIDs(aggregated), "aggregated alone")
		})
	}
}

// TestLookupBackendCapabilities_MonitorDecides proves the health monitor's
// status decides over the registry's, as it does for the lists, and that a
// degraded backend is in the lists.
func TestLookupBackendCapabilities_MonitorDecides(t *testing.T) {
	t.Parallel()
	c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{
		{ID: "b1", HealthStatus: vmcp.BackendHealthy},
		{ID: "b2", HealthStatus: vmcp.BackendUnhealthy},
	}, "t1", "t2")
	c.health = fakeStatusProvider{"b1": vmcp.BackendUnhealthy, "b2": vmcp.BackendDegraded}
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(twoBackendView(), nil).Times(2)

	_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b1")
	require.ErrorIs(t, err, vmcp.ErrBackendUnavailable, "the monitor says unhealthy, whatever the registry says")
	caps, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b2")
	require.NoError(t, err, "the monitor says degraded, whatever the registry says")
	assert.Equal(t, []vmcp.Tool{toolOn("t2", "b2")}, caps.Tools)
}

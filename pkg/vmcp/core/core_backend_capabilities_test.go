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

// recordAggregations makes m's aggregator answer view and err, and returns the
// backend IDs of every aggregation it is asked for, in order.
func recordAggregations(m *coreMocks, view *aggregator.AggregatedCapabilities, err error) *[][]string {
	var sets [][]string
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			sets = append(sets, backendIDs(backends))
			return view, err
		}).AnyTimes()
	return &sets
}

// TestLookupBackendCapabilities_NotFound proves that an unknown backend, one
// that offers identity nothing, one with no items, one whose discovery fails,
// and a hidden one the lists leave out for its health all get vmcp.ErrNotFound,
// after the same aggregations: the backends the lists include, then the whole
// registry. So the time an answer takes does not tell them apart either.
func TestLookupBackendCapabilities_NotFound(t *testing.T) {
	t.Parallel()
	view := twoBackendView()
	view.Tools = append(view.Tools, toolOn("t3", "b3"))
	failed := fmt.Errorf("failed to query backends: %w", aggregator.ErrNoBackendCapabilities)
	for _, tc := range []struct {
		name      string
		backendID string
		view      *aggregator.AggregatedCapabilities
		aggErr    error
		allowed   []string
	}{
		{name: "unknown", backendID: "missing", view: view, allowed: []string{"t1", "t2"}},
		{name: "empty id", backendID: "", view: view, allowed: []string{"t1", "t2", "backendless"}},
		{name: "nothing admitted", backendID: "b1", view: view, allowed: []string{"t2"}},
		{name: "failed discovery", backendID: "b1", view: &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{toolOn("t2", "b2")},
		}, allowed: []string{"t1", "t2"}},
		{name: "every backend failed discovery", backendID: "b1", aggErr: failed},
		{name: "hidden and unhealthy", backendID: "b3", view: view, allowed: []string{"t1", "t2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{
				{ID: "b1"}, {ID: "b2"}, {ID: "b3", HealthStatus: vmcp.BackendUnhealthy},
			}, tc.allowed...)
			sets := recordAggregations(m, tc.view, tc.aggErr)
			_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), tc.backendID)
			require.ErrorIs(t, err, vmcp.ErrNotFound)
			assert.NotErrorIs(t, err, vmcp.ErrBackendUnavailable)
			assert.Equal(t, [][]string{{"b1", "b2"}, {"b1", "b2", "b3"}}, *sets,
				"the lists' backends, then the whole registry")
		})
	}
}

// TestLookupBackendCapabilities_Errors proves that an aggregation failure other
// than every backend failing discovery is returned as it is, for a known
// backend and an unknown name alike, and that once the request's context has
// ended, an aggregation in which no backend answered is an error too: the
// backends did not fail discovery, the request went away. None is
// vmcp.ErrNotFound.
func TestLookupBackendCapabilities_Errors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	for _, tc := range []struct {
		name      string
		backendID string
		aggErr    error
		cancelled bool
		want      error
	}{
		{name: "aggregation fails, known backend", backendID: "b1", aggErr: boom, want: boom},
		{name: "aggregation fails, unknown name", backendID: "missing", aggErr: boom, want: boom},
		{name: "no backend answered a cancelled request", backendID: "b1", cancelled: true,
			aggErr: fmt.Errorf("x: %w", aggregator.ErrNoBackendCapabilities), want: aggregator.ErrNoBackendCapabilities},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{{ID: "b1"}}, "t1")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if tc.cancelled {
				cancel()
			}
			m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, tc.aggErr).Times(1)
			_, err := c.LookupBackendCapabilities(ctx, cedarIdentity(), tc.backendID)
			require.ErrorIs(t, err, tc.want)
			assert.NotErrorIs(t, err, vmcp.ErrNotFound)
		})
	}
}

// TestLookupBackendCapabilities_LeftOutForHealth proves that a backend the
// lists leave out for its health status is decided from an aggregation of the
// whole registry, as ListBackends aggregates it, so conflict resolution decides
// its items as it does there: in identity's view, by any kind of item including
// resource templates, it gets vmcp.ErrBackendUnavailable; out of it, or
// unreachable, vmcp.ErrNotFound.
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
			// b3 is unhealthy too, so the whole registry differs from the
			// backends the lists include plus b2.
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{
				{ID: "b1", HealthStatus: vmcp.BackendHealthy},
				{ID: "b2", HealthStatus: vmcp.BackendUnhealthy},
				{ID: "b3", HealthStatus: vmcp.BackendUnhealthy},
			}, tc.allowed...)
			sets := recordAggregations(m, tc.view, tc.aggErr)
			_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b2")
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, [][]string{{"b1"}, {"b1", "b2", "b3"}}, *sets, "the lists' backends, then the whole registry")
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
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(twoBackendView(), nil).Times(3)

	_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b1")
	require.ErrorIs(t, err, vmcp.ErrBackendUnavailable, "the monitor says unhealthy, whatever the registry says")
	caps, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), "b2")
	require.NoError(t, err, "the monitor says degraded, whatever the registry says")
	assert.Equal(t, []vmcp.Tool{toolOn("t2", "b2")}, caps.Tools)
}

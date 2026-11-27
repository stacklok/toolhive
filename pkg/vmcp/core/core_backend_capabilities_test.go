// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	require.Len(t, caps.ListErrors, 1)
	assert.ErrorIs(t, caps.ListErrors["prompts/list"], vmcp.ErrBackendUnavailable,
		"only b1's own failed list, as an error that is not a refusal")

	var decided []string
	for _, call := range authorizer.calls {
		decided = append(decided, call.resourceID)
	}
	slices.Sort(decided)
	// A denied tool is decided twice: tool:read, then tool:call, which implies it.
	assert.Equal(t, []string{"p1", "res://b1", "t1", "t1_denied", "tmpl://b1/{x}"}, slices.Compact(decided),
		"admission runs over the backend's own items only")
}

// recordAggregations makes m's aggregator answer view and err, and returns the
// backend IDs of every aggregation it is asked for, in order.
func recordAggregations(m *coreMocks, view *aggregator.AggregatedCapabilities, err error) *[][]string {
	return recordAggregationsBy(m, func([]vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
		return view, err
	})
}

// recordAggregationsBy is recordAggregations with an answer that depends on
// the backends aggregated.
func recordAggregationsBy(
	m *coreMocks, answer func([]vmcp.Backend) (*aggregator.AggregatedCapabilities, error),
) *[][]string {
	var sets [][]string
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			sets = append(sets, backendIDs(backends))
			return answer(backends)
		}).AnyTimes()
	return &sets
}

// errNoBackendAnswered is the error an aggregation gives when every backend in it
// failed discovery.
var errNoBackendAnswered = fmt.Errorf("failed to query backends: %w", aggregator.ErrNoBackendCapabilities)

// TestLookupBackendCapabilities_NotFound proves that an unknown backend, one
// that offers identity nothing, one with no items, one whose discovery fails
// while others answer, and a hidden one the lists leave out for its health all
// get vmcp.ErrNotFound after the same aggregations: the backends the lists
// include, then the whole registry.
func TestLookupBackendCapabilities_NotFound(t *testing.T) {
	t.Parallel()
	view := twoBackendView()
	view.Tools = append(view.Tools, toolOn("t3", "b3"))
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

// TestLookupBackendCapabilities_Errors proves that an aggregation failure is
// returned as it is, for a known backend and an unknown name alike, so the two
// get the same answer after the same aggregations, and never as
// vmcp.ErrNotFound: a failure other than no backend answering, at once; no
// backend answering, as an outage, after one aggregation when every backend is
// healthy, and after the lists' backends and the whole registry otherwise; and
// a request whose context ends before or during the second aggregation, with
// the context's error, since the request went away. An empty registry is not an
// outage: every name gets vmcp.ErrNotFound without aggregating, as
// LookupBackend answers it.
func TestLookupBackendCapabilities_Errors(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	healthy := []vmcp.Backend{{ID: "b1"}}
	oneUnhealthy := []vmcp.Backend{{ID: "b1"}, {ID: "b2", HealthStatus: vmcp.BackendUnhealthy}}
	for _, tc := range []struct {
		name      string
		backends  []vmcp.Backend
		backendID string
		aggErr    error
		// cancelled ends the context before the lookup; cancelInSweep ends
		// it during the aggregation numbered so, after which an aggregation
		// fails with the context's error.
		cancelled     bool
		cancelInSweep int
		want          error
		sweeps        int
	}{
		{name: "aggregation fails, known backend", backends: healthy, backendID: "b1",
			aggErr: boom, want: boom, sweeps: 1},
		{name: "aggregation fails, unknown name", backends: healthy, backendID: "missing",
			aggErr: boom, want: boom, sweeps: 1},
		{name: "no backend answers, all healthy, known backend", backends: healthy, backendID: "b1",
			aggErr: errNoBackendAnswered, want: aggregator.ErrNoBackendCapabilities, sweeps: 1},
		{name: "no backend answers, all healthy, unknown name", backends: healthy, backendID: "missing",
			aggErr: errNoBackendAnswered, want: aggregator.ErrNoBackendCapabilities, sweeps: 1},
		{name: "no backend answers, known backend", backends: oneUnhealthy, backendID: "b1",
			aggErr: errNoBackendAnswered, want: aggregator.ErrNoBackendCapabilities, sweeps: 2},
		{name: "no backend answers, unknown name", backends: oneUnhealthy, backendID: "missing",
			aggErr: errNoBackendAnswered, want: aggregator.ErrNoBackendCapabilities, sweeps: 2},
		{name: "no backend answered a cancelled request", backends: oneUnhealthy, backendID: "b1",
			cancelled: true, aggErr: errNoBackendAnswered, want: aggregator.ErrNoBackendCapabilities, sweeps: 1},
		{name: "cancelled once no backend answered the first aggregation", backends: oneUnhealthy, backendID: "b1",
			cancelInSweep: 1, aggErr: errNoBackendAnswered, want: aggregator.ErrNoBackendCapabilities, sweeps: 1},
		{name: "cancelled during the second aggregation", backends: oneUnhealthy, backendID: "b1",
			cancelInSweep: 2, aggErr: errNoBackendAnswered, want: context.Canceled, sweeps: 2},
		{name: "empty registry, unknown name", backendID: "missing", want: vmcp.ErrNotFound},
		{name: "empty registry, empty id", backendID: "", want: vmcp.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, tc.backends, "t1")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if tc.cancelled {
				cancel()
			}
			n := 0
			sets := recordAggregationsBy(m, func([]vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
				n++
				if n == tc.cancelInSweep {
					cancel()
				}
				if n > 1 && ctx.Err() != nil {
					return nil, fmt.Errorf("failed to query backends: %w", ctx.Err())
				}
				return nil, tc.aggErr
			})
			_, err := c.LookupBackendCapabilities(ctx, cedarIdentity(), tc.backendID)
			require.ErrorIs(t, err, tc.want)
			if !errors.Is(tc.want, vmcp.ErrNotFound) {
				assert.NotErrorIs(t, err, vmcp.ErrNotFound)
			}
			assert.Len(t, *sets, tc.sweeps)
		})
	}
}

// TestLookupBackendCapabilities_DiscoveryRecovers proves that a backend whose
// discovery fails in the first aggregation and succeeds in a later one is not
// answered vmcp.ErrNotFound when identity can see it. With every backend
// healthy, the one aggregation decides, so its outage is returned. With an
// unhealthy backend, the whole registry's aggregation finds the listed
// backend, which gets vmcp.ErrBackendUnavailable, a retry; an unknown name and
// a hidden backend still get vmcp.ErrNotFound after the same aggregations.
func TestLookupBackendCapabilities_DiscoveryRecovers(t *testing.T) {
	t.Parallel()
	recovered := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{toolOn("t1", "b1"), toolOn("t3", "b3")},
	}
	for _, tc := range []struct {
		name      string
		b2Health  vmcp.BackendHealthStatus
		backendID string
		want      error
		sweeps    int
	}{
		{name: "all healthy", b2Health: vmcp.BackendHealthy, backendID: "b1",
			want: aggregator.ErrNoBackendCapabilities, sweeps: 1},
		{name: "all healthy, unknown name", b2Health: vmcp.BackendHealthy, backendID: "missing",
			want: aggregator.ErrNoBackendCapabilities, sweeps: 1},
		{name: "one unhealthy", b2Health: vmcp.BackendUnhealthy, backendID: "b1",
			want: vmcp.ErrBackendUnavailable, sweeps: 2},
		{name: "one unhealthy, unknown name", b2Health: vmcp.BackendUnhealthy, backendID: "missing",
			want: vmcp.ErrNotFound, sweeps: 2},
		{name: "one unhealthy, hidden", b2Health: vmcp.BackendUnhealthy, backendID: "b3",
			want: vmcp.ErrNotFound, sweeps: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{
				{ID: "b1"}, {ID: "b2", HealthStatus: tc.b2Health}, {ID: "b3"},
			}, "t1")
			n := 0
			sets := recordAggregationsBy(m, func([]vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
				n++
				if n == 1 {
					return nil, errNoBackendAnswered
				}
				return recovered, nil
			})
			_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), tc.backendID)
			require.ErrorIs(t, err, tc.want)
			if !errors.Is(tc.want, vmcp.ErrNotFound) {
				assert.NotErrorIs(t, err, vmcp.ErrNotFound)
			}
			assert.Len(t, *sets, tc.sweeps)
		})
	}
}

// TestLookupBackendCapabilities_ListedBackendsDown proves that when every
// backend the lists include fails discovery but another backend answers, the
// whole registry decides as usual: an unhealthy backend in identity's view
// gets vmcp.ErrBackendUnavailable, and a listed one vmcp.ErrNotFound, as any
// backend whose discovery fails while others answer.
func TestLookupBackendCapabilities_ListedBackendsDown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		backendID string
		want      error
	}{
		{backendID: "b2", want: vmcp.ErrBackendUnavailable},
		{backendID: "b1", want: vmcp.ErrNotFound},
	} {
		t.Run(tc.backendID, func(t *testing.T) {
			t.Parallel()
			c, m, _ := newBackendCapabilitiesCore(t, []vmcp.Backend{
				{ID: "b1", HealthStatus: vmcp.BackendHealthy},
				{ID: "b2", HealthStatus: vmcp.BackendUnhealthy},
			}, "t2")
			recordAggregationsBy(m, func(backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
				if len(backends) == 1 {
					return nil, errNoBackendAnswered
				}
				return &aggregator.AggregatedCapabilities{Tools: []vmcp.Tool{toolOn("t2", "b2")}}, nil
			})
			_, err := c.LookupBackendCapabilities(context.Background(), cedarIdentity(), tc.backendID)
			require.ErrorIs(t, err, tc.want)
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
		{name: "unreachable", view: &aggregator.AggregatedCapabilities{Tools: []vmcp.Tool{toolOn("t1", "b1")}},
			allowed: []string{"t1"}, want: vmcp.ErrNotFound},
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

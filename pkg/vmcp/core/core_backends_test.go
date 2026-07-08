// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// backendIDs extracts the IDs from a backend slice for order-insensitive assertions.
func backendIDs(backends []vmcp.Backend) []string {
	ids := make([]string, len(backends))
	for i := range backends {
		ids[i] = backends[i].ID
	}
	return ids
}

// toolOn returns a backend tool advertised by the given backend.
func toolOn(name, backendID string) vmcp.Tool {
	return vmcp.Tool{Name: name, BackendID: backendID}
}

// denyExcept builds an Admission that allows only the named capabilities (by
// resourceID: tool name / resource URI / prompt name) and default-denies the rest,
// so tests can drive per-backend visibility. Mirrors admission_test.go's usage.
func denyExcept(allowed ...string) Admission {
	results := make(map[string]mockResult, len(allowed))
	for _, id := range allowed {
		results[id] = mockResult{authorized: true}
	}
	return newCedarAdmission(&mockAuthorizer{results: results})
}

// TestListBackends_AdminMode_ReturnsAllIncludingUnhealthy proves the admin view
// (filterUnauthorized=false) returns every group backend from the registry —
// including an unhealthy one — with no aggregation and no health filter. A nil
// identity is accepted. The absence of an AggregateCapabilities expectation on the
// mock asserts the admin path never aggregates (gomock fails on an unexpected call).
func TestListBackends_AdminMode_ReturnsAllIncludingUnhealthy(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	all := []vmcp.Backend{
		{ID: "b1", HealthStatus: vmcp.BackendHealthy},
		{ID: "b2", HealthStatus: vmcp.BackendUnhealthy},
	}
	m.reg.EXPECT().List(gomock.Any()).Return(all)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.ListBackends(context.Background(), nil, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b1", "b2"}, backendIDs(got),
		"admin view must include all group backends, including the unhealthy one")
}

// TestListBackends_Authorized_FiltersByAdmittedCapability proves the authorized
// view (filterUnauthorized=true) includes a backend only when the identity is
// admitted at least one of its capabilities: b1's tool is allowed, b2's is denied,
// so only b1 appears.
func TestListBackends_Authorized_FiltersByAdmittedCapability(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	all := []vmcp.Backend{{ID: "b1"}, {ID: "b2"}}
	m.reg.EXPECT().List(gomock.Any()).Return(all)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{toolOn("tool_b1", "b1"), toolOn("tool_b2", "b2")},
		}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	c.(*coreVMCP).admission = denyExcept("tool_b1")

	got, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1"}, backendIDs(got),
		"only the backend with an admitted capability should appear")
}

// TestListBackends_Authorized_UnionsAcrossCapabilityKinds proves visibility is the
// UNION over tools, resources, and prompts: a backend reachable only via a resource
// (no admitted tool) or only via a prompt still appears.
func TestListBackends_Authorized_UnionsAcrossCapabilityKinds(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	all := []vmcp.Backend{{ID: "b1"}, {ID: "b2"}, {ID: "b3"}}
	m.reg.EXPECT().List(gomock.Any()).Return(all)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{
			Tools:     []vmcp.Tool{toolOn("tool_b1", "b1")},
			Resources: []vmcp.Resource{{URI: "res://b2", BackendID: "b2"}},
			Prompts:   []vmcp.Prompt{{Name: "prompt_b3", BackendID: "b3"}},
		}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	// Default allow-all admission (baseConfig has no Authz) admits every capability.

	got, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b1", "b2", "b3"}, backendIDs(got),
		"a backend visible only via a resource or prompt must still appear")
}

// TestListBackends_Authorized_AggregatesFullRegistryNoHealthFilter is the
// load-bearing test for "health is a status, not a visibility filter": the
// authorized path must aggregate over the FULL registry, not the
// filterHealthyBackends subset. It captures the backend slice handed to
// AggregateCapabilities and asserts the unhealthy backend is present.
func TestListBackends_Authorized_AggregatesFullRegistryNoHealthFilter(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	all := []vmcp.Backend{
		{ID: "b1", HealthStatus: vmcp.BackendHealthy},
		{ID: "b2", HealthStatus: vmcp.BackendUnhealthy},
	}
	m.reg.EXPECT().List(gomock.Any()).Return(all)

	var aggregated []vmcp.Backend
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, backends []vmcp.Backend) (*aggregator.AggregatedCapabilities, error) {
			aggregated = backends
			return &aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{toolOn("tool_b1", "b1"), toolOn("tool_b2", "b2")},
			}, nil
		})

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b1", "b2"}, backendIDs(aggregated),
		"authorized aggregation must run over the full registry (no health filter dropping the unhealthy backend)")
	assert.ElementsMatch(t, []string{"b1", "b2"}, backendIDs(got))
}

// TestListBackends_Authorized_ZeroCapabilityBackendHiddenButAdminShowsIt pins the
// decided corner case: a backend that contributes no capabilities is vacuously
// hidden in the authorized view (>=1 rule) yet still appears in the admin view.
func TestListBackends_Authorized_ZeroCapabilityBackendHiddenButAdminShowsIt(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	all := []vmcp.Backend{{ID: "b1"}, {ID: "b2"}} // b2 is tool-less / contributes nothing
	m.reg.EXPECT().List(gomock.Any()).Return(all).Times(2)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{toolOn("tool_b1", "b1")}, // nothing for b2
		}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	authorized, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1"}, backendIDs(authorized),
		"a zero-capability backend is vacuously hidden in the authorized view")

	admin, err := c.ListBackends(context.Background(), nil, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b1", "b2"}, backendIDs(admin),
		"the same zero-capability backend still appears in the admin view")
}

// TestListBackends_EmptyGroupReturnsEmpty proves that an empty group (no backends
// in the registry) returns a non-nil empty slice in BOTH modes without aggregating.
// The authorized path must short-circuit before the aggregator, whose "no backends
// returned capabilities" sentinel would otherwise turn an empty group into a hard
// error and make the two views disagree. The absence of an AggregateCapabilities
// expectation asserts no aggregation happens.
func TestListBackends_EmptyGroupReturnsEmpty(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{}).Times(2)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	admin, err := c.ListBackends(context.Background(), nil, false)
	require.NoError(t, err)
	assert.NotNil(t, admin)
	assert.Empty(t, admin)

	authorized, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err, "an empty group must be an empty answer, not an aggregation error")
	assert.NotNil(t, authorized)
	assert.Empty(t, authorized)
}

// TestListBackends_Authorized_NilIdentityIsAnonymous pins the shipped semantic that
// a nil identity in authorized mode is anonymous (consistent with ListTools et al.),
// NOT an error — even though issue #5741's prose calls identity "required". Under the
// default allow-all admission the anonymous-visible set is every backend with a
// capability.
func TestListBackends_Authorized_NilIdentityIsAnonymous(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "b1"}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{Tools: []vmcp.Tool{toolOn("tool_b1", "b1")}}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.ListBackends(context.Background(), nil, true)
	require.NoError(t, err, "nil identity is anonymous, not an error")
	assert.Equal(t, []string{"b1"}, backendIDs(got))
}

// TestListBackends_Authorized_BackendlessToolDoesNotAddVisibility pins the decision
// to union over agg.Tools (real backend capabilities) rather than advertisedTools:
// a tool with no backing backend id — the shape a composite/workflow tool takes,
// since it routes to no single backend — must not make any backend visible.
func TestListBackends_Authorized_BackendlessToolDoesNotAddVisibility(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	all := []vmcp.Backend{{ID: "b1"}, {ID: "b2"}}
	m.reg.EXPECT().List(gomock.Any()).Return(all)
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{
				toolOn("tool_b1", "b1"),      // real backend tool
				{Name: "composite_workflow"}, // no BackendID (composite-shaped)
			},
		}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"b1"}, backendIDs(got),
		"a backendless (composite) tool must not surface b2 or a phantom empty-id backend")
}

// TestListBackends_Authorized_EmptyIsNonNil proves the authorized path returns a
// non-nil empty slice (not nil) when the identity is admitted nothing.
func TestListBackends_Authorized_EmptyIsNonNil(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "b1"}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{Tools: []vmcp.Tool{toolOn("tool_b1", "b1")}}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	c.(*coreVMCP).admission = denyExcept() // deny everything

	got, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestListBackends_Authorized_AggregationErrorFailsClosed proves an aggregation
// error propagates (wrapped) and yields no partial backend set.
func TestListBackends_Authorized_AggregationErrorFailsClosed(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "b1"}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom"))

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.ListBackends(context.Background(), cedarIdentity(), true)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "aggregation")
}

// TestLookupBackend_ResolvesAuthorizedBackend proves LookupBackend returns a
// backend that is in the identity's authorized view.
func TestLookupBackend_ResolvesAuthorizedBackend(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "b1", Name: "Backend One"}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
		&aggregator.AggregatedCapabilities{Tools: []vmcp.Tool{toolOn("tool_b1", "b1")}}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.LookupBackend(context.Background(), cedarIdentity(), "b1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "b1", got.ID)
	assert.Equal(t, "Backend One", got.Name)
}

// TestLookupBackend_NotFound proves LookupBackend returns vmcp.ErrNotFound for an
// unknown id and for an id that exists in the group but is unauthorized.
func TestLookupBackend_NotFound(t *testing.T) {
	t.Parallel()

	t.Run("unknown id", func(t *testing.T) {
		t.Parallel()
		cfg, m := baseConfig(t)
		m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "b1"}})
		m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
			&aggregator.AggregatedCapabilities{Tools: []vmcp.Tool{toolOn("tool_b1", "b1")}}, nil)

		c, err := New(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })

		got, err := c.LookupBackend(context.Background(), cedarIdentity(), "does-not-exist")
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, vmcp.ErrNotFound)
	})

	t.Run("unauthorized id", func(t *testing.T) {
		t.Parallel()
		cfg, m := baseConfig(t)
		m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: "b1"}, {ID: "b2"}})
		m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(
			&aggregator.AggregatedCapabilities{
				Tools: []vmcp.Tool{toolOn("tool_b1", "b1"), toolOn("tool_b2", "b2")},
			}, nil)

		c, err := New(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		c.(*coreVMCP).admission = denyExcept("tool_b1") // b2 unauthorized

		got, err := c.LookupBackend(context.Background(), cedarIdentity(), "b2")
		require.Error(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, vmcp.ErrNotFound)
	})
}

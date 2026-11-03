// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// TestDiscover_BackendInstructions_FollowAdmission verifies that Discover
// returns the instructions of exactly the backends the identity is admitted
// to: a backend with no admitted capability is absent even though the
// aggregate carries its text, and an empty text never produces an entry.
func TestDiscover_BackendInstructions_FollowAdmission(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	backends := []vmcp.Backend{
		{ID: testBackendID, HealthStatus: vmcp.BackendHealthy},
		{ID: "silent", HealthStatus: vmcp.BackendHealthy},
		{ID: "unreachable-for-identity", HealthStatus: vmcp.BackendHealthy},
	}
	m.reg.EXPECT().List(gomock.Any()).Return(backends).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:   []vmcp.Tool{backendTool("echo")},
		Prompts: []vmcp.Prompt{{Name: "p1", BackendID: "silent"}},
		BackendInstructions: map[string]string{
			testBackendID:              "Call echo once.",
			"unreachable-for-identity": "Must stay hidden: no admitted capability.",
		},
		RoutingTable: &vmcp.RoutingTable{},
	}, nil).AnyTimes()

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	caps, err := c.Discover(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, caps.HasTools)
	assert.Equal(t, map[string]string{testBackendID: "Call echo once."}, caps.BackendInstructions)
}

// TestDiscover_BackendInstructions_DeniedIdentity is the security case: a
// denied identity must not receive the instructions of the backend whose only
// capability it is denied, mirroring TestDiscover_DeniedIdentityHidesTools.
func TestDiscover_BackendInstructions_DeniedIdentity(t *testing.T) {
	t.Parallel()
	_, m := baseConfig(t)

	authorizer := &mockAuthorizer{results: map[string]mockResult{"echo": {authorized: false}}}
	c := checkCore(m, newCedarAdmission(authorizer))
	expectAggregationAnyTimes(m, &aggregator.AggregatedCapabilities{
		Tools:               []vmcp.Tool{backendTool("echo")},
		BackendInstructions: map[string]string{testBackendID: "Call echo once."},
		RoutingTable:        &vmcp.RoutingTable{},
	})

	caps, err := c.Discover(t.Context(), cedarIdentity())
	require.NoError(t, err)
	assert.False(t, caps.HasTools)
	assert.Nil(t, caps.BackendInstructions,
		"a denied identity must not see the instructions of a backend it cannot use")
}

// TestDiscover_NoInstructions verifies an aggregate without instructions
// leaves the map nil, so DiscoverCapabilities stays comparable to the zero
// value for callers that have no text to advertise.
func TestDiscover_NoInstructions(t *testing.T) {
	t.Parallel()
	cfg, m := baseConfig(t)

	m.reg.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}})
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{backendTool("echo")},
		RoutingTable: &vmcp.RoutingTable{},
	}, nil)

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	caps, err := c.Discover(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, caps.BackendInstructions)
}

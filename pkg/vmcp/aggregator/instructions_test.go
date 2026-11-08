// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

func withInstructions(text string) func(*vmcp.CapabilityList) {
	return func(c *vmcp.CapabilityList) { c.Instructions = text }
}

// TestDefaultAggregator_BackendInstructions verifies the per-backend
// instructions text survives the pipeline keyed by backend ID, and that a
// backend without instructions has no entry.
func TestDefaultAggregator_BackendInstructions(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockClient := mocks.NewMockBackendClient(ctrl)
	backends := []vmcp.Backend{
		newTestBackend("backend1", withBackendName("Backend 1")),
		newTestBackend("backend2", withBackendName("Backend 2")),
	}
	caps1 := newTestCapabilityList(
		withTools(newTestTool("tool1", "backend1")),
		withInstructions("Use tool1 first."))
	caps2 := newTestCapabilityList(withTools(newTestTool("tool2", "backend2")))

	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend1" {
				return caps1, nil
			}
			return caps2, nil
		}).Times(2)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil)
	result, err := agg.AggregateCapabilities(context.Background(), backends)
	require.NoError(t, err)

	assert.Equal(t, map[string]string{"backend1": "Use tool1 first."}, result.BackendInstructions)
	assert.Len(t, result.Tools, 2, "tools are aggregated as before")
}

// TestDefaultAggregator_QueryCapabilities_Instructions verifies the raw text
// is copied onto BackendCapabilities by the per-backend query.
func TestDefaultAggregator_QueryCapabilities_Instructions(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	mockClient := mocks.NewMockBackendClient(ctrl)
	mockClient.EXPECT().ListCapabilities(gomock.Any(), gomock.Any()).
		Return(newTestCapabilityList(withInstructions("Say hello.")), nil)

	agg := NewDefaultAggregator(mockClient, nil, nil, nil)
	caps, err := agg.QueryCapabilities(context.Background(), newTestBackend("backend1"))
	require.NoError(t, err)
	assert.Equal(t, "Say hello.", caps.Instructions)
}

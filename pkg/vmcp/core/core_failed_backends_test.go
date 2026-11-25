// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

func TestAggFailedBackends(t *testing.T) {
	t.Parallel()

	agg := &aggregator.AggregatedCapabilities{
		Metadata: &aggregator.AggregationMetadata{FailedBackends: []string{"down"}},
		UnavailableLists: map[string][]string{
			"b1": {"prompts/list"},
			"b2": {"resources/templates/list"},
		},
	}

	tests := []struct {
		name    string
		agg     *aggregator.AggregatedCapabilities
		methods []string
		want    []string
	}{
		{name: "nil aggregation", agg: nil, methods: []string{"tools/list"}, want: nil},
		{name: "nothing failed", agg: &aggregator.AggregatedCapabilities{}, methods: []string{"tools/list"}, want: nil},
		{
			// A backend that answered but could not list prompts has unknown
			// prompts, so a prompts resync must keep them.
			name: "prompts include a backend whose prompts/list was unavailable",
			agg:  agg, methods: []string{"prompts/list"}, want: []string{"b1", "down"},
		},
		{
			name: "resources include a backend whose template list was unavailable",
			agg:  agg, methods: []string{"resources/list", "resources/templates/list"}, want: []string{"b2", "down"},
		},
		{
			// b1's and b2's tools were listed, so they are not failed for tools.
			name: "tools include only backends that failed their query",
			agg:  agg, methods: []string{"tools/list"}, want: []string{"down"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, aggFailedBackends(tc.agg, tc.methods...))
		})
	}
}

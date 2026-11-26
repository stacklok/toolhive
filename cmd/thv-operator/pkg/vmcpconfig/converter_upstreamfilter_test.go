// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcpconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/authserver/upstreamfilter"
)

func TestConvertUpstreamFilter(t *testing.T) {
	t.Parallel()

	t.Run("nil input returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, ConvertUpstreamFilter(nil))
	})

	t.Run("full config translates every field", func(t *testing.T) {
		t.Parallel()
		input := &mcpv1beta1.AuthServerUpstreamFilterConfig{
			Claim: "cognito:groups",
			Rules: []mcpv1beta1.UpstreamFilterGroupRule{
				{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira", "confluence"}},
				{Groups: []string{"sales"}, UpstreamProviders: []string{"salesforce"}},
			},
			DefaultUpstreams: []string{"jira"},
		}
		want := &upstreamfilter.GroupBasedFilterConfig{
			Claim: "cognito:groups",
			Rules: []upstreamfilter.GroupRule{
				{Groups: []string{"engineering"}, UpstreamProviders: []string{"jira", "confluence"}},
				{Groups: []string{"sales"}, UpstreamProviders: []string{"salesforce"}},
			},
			DefaultUpstreams: []string{"jira"},
		}
		assert.Equal(t, want, ConvertUpstreamFilter(input))
	})

	t.Run("empty Claim and empty DefaultUpstreams preserved", func(t *testing.T) {
		t.Parallel()
		input := &mcpv1beta1.AuthServerUpstreamFilterConfig{
			Rules: []mcpv1beta1.UpstreamFilterGroupRule{
				{Groups: []string{"g1"}, UpstreamProviders: []string{"u1"}},
			},
		}
		got := ConvertUpstreamFilter(input)
		require.NotNil(t, got)
		assert.Equal(t, "", got.Claim)
		assert.Empty(t, got.DefaultUpstreams)
	})

	t.Run("slices are cloned defensively", func(t *testing.T) {
		t.Parallel()
		input := &mcpv1beta1.AuthServerUpstreamFilterConfig{
			Rules: []mcpv1beta1.UpstreamFilterGroupRule{
				{Groups: []string{"g1"}, UpstreamProviders: []string{"u1"}},
			},
			DefaultUpstreams: []string{"u2"},
		}
		got := ConvertUpstreamFilter(input)
		require.NotNil(t, got)

		// Mutate caller-owned slices after conversion.
		input.Rules[0].Groups[0] = "mutated"
		input.Rules[0].UpstreamProviders[0] = "mutated"
		input.DefaultUpstreams[0] = "mutated"

		assert.Equal(t, "g1", got.Rules[0].Groups[0], "returned Groups must not alias input")
		assert.Equal(t, "u1", got.Rules[0].UpstreamProviders[0], "returned UpstreamProviders must not alias input")
		assert.Equal(t, "u2", got.DefaultUpstreams[0], "returned DefaultUpstreams must not alias input")
	})
}

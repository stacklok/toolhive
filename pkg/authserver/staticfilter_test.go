// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
)

func TestStaticUpstreamFilter(t *testing.T) {
	t.Parallel()

	t.Run("returns the configured keep set regardless of principal and configured set", func(t *testing.T) {
		t.Parallel()

		filter := NewStaticUpstreamFilter([]string{"github", "slack"})

		keep, err := filter.FilterUpstreams(
			context.Background(),
			auth.PrincipalInfo{PlatformUserID: "user-1"},
			[]string{"github", "slack", "notion"},
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"github", "slack"}, keep)
	})

	t.Run("empty keep set narrows to just the first upstream", func(t *testing.T) {
		t.Parallel()

		filter := NewStaticUpstreamFilter(nil)

		keep, err := filter.FilterUpstreams(
			context.Background(),
			auth.PrincipalInfo{PlatformUserID: "user-1"},
			[]string{"github"},
		)
		require.NoError(t, err)
		assert.Empty(t, keep)
	})

	t.Run("caller mutations cannot change filtering behavior", func(t *testing.T) {
		t.Parallel()

		input := []string{"github"}
		filter := NewStaticUpstreamFilter(input)

		// Mutating the constructor argument after construction must not leak in.
		input[0] = "mutated-input"

		keep, err := filter.FilterUpstreams(context.Background(), auth.PrincipalInfo{}, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"github"}, keep)

		// Mutating a returned slice must not affect subsequent calls.
		keep[0] = "mutated-output"

		again, err := filter.FilterUpstreams(context.Background(), auth.PrincipalInfo{}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"github"}, again)
	})
}

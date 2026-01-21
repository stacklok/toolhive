// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackendInfoContext(t *testing.T) {
	t.Parallel()

	t.Run("BackendInfo can be added and retrieved from context", func(t *testing.T) {
		t.Parallel()

		// Create a BackendInfo
		info := &BackendInfo{
			BackendName: "test-backend",
		}

		// Add it to context
		ctx := WithBackendInfo(context.Background(), info)

		// Retrieve it
		retrieved, ok := BackendInfoFromContext(ctx)
		require.True(t, ok, "BackendInfo should be in context")
		require.NotNil(t, retrieved, "BackendInfo should not be nil")
		assert.Equal(t, "test-backend", retrieved.BackendName)

		// Verify it's the same pointer
		assert.Same(t, info, retrieved, "Should be the same BackendInfo pointer")
	})

	t.Run("BackendInfo can be mutated through context", func(t *testing.T) {
		t.Parallel()

		// Create empty BackendInfo
		info := &BackendInfo{}

		// Add to context
		ctx := WithBackendInfo(context.Background(), info)

		// Retrieve and mutate
		retrieved, ok := BackendInfoFromContext(ctx)
		require.True(t, ok)
		retrieved.BackendName = "mutated-backend"

		// Verify original was mutated
		assert.Equal(t, "mutated-backend", info.BackendName)
	})

	t.Run("Missing BackendInfo returns false", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		retrieved, ok := BackendInfoFromContext(ctx)
		assert.False(t, ok, "Should return false when not in context")
		assert.Nil(t, retrieved, "Should return nil when not in context")
	})

	t.Run("BackendInfo survives context derivation", func(t *testing.T) {
		t.Parallel()

		// Create BackendInfo and add to context
		info := &BackendInfo{BackendName: "original"}
		ctx := WithBackendInfo(context.Background(), info)

		// Derive a new context with additional value
		type key struct{}
		derivedCtx := context.WithValue(ctx, key{}, "some-value")

		// BackendInfo should still be accessible
		retrieved, ok := BackendInfoFromContext(derivedCtx)
		require.True(t, ok, "BackendInfo should survive context derivation")
		assert.Equal(t, "original", retrieved.BackendName)
	})
}

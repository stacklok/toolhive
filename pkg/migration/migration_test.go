// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"sync"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/groups"
)

// TestEnsureDefaultGroupExists_Concurrent reproduces the startup race where
// several `thv` processes initialize the default group at once against a shared
// state directory. Exactly one caller wins the exclusive create; the rest must
// treat groups.ErrGroupAlreadyExists as success instead of a fatal error.
//
// See https://github.com/stacklok/toolhive/issues/6359.
func TestEnsureDefaultGroupExists_Concurrent(t *testing.T) {
	// Redirect the XDG state dir to a fresh directory and restore the package
	// globals afterwards. Cleanups run LIFO, so the env restore registered by
	// t.Setenv runs first, then xdg.Reload re-reads the original environment.
	t.Cleanup(func() { xdg.Reload() })
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()

	const callers = 16
	errs := make([]error, callers)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = ensureDefaultGroupExists(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoError(t, err, "caller %d should not observe a fatal error", i)
	}

	// The default group must exist exactly once after the dust settles.
	mgr, err := groups.NewManager()
	require.NoError(t, err)
	exists, err := mgr.Exists(context.Background(), groups.DefaultGroupName)
	require.NoError(t, err)
	assert.True(t, exists, "default group should exist after concurrent initialization")
}

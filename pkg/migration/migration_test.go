// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/groups"
)

// setupIsolatedState points XDG_STATE_HOME at a fresh temp directory so tests
// never touch the developer's real state dir. xdg caches StateHome at package
// init, so t.Setenv alone is not enough: xdg.Reload() must be called to re-read
// the env var, and again on cleanup to restore the cached value.
func setupIsolatedState(t *testing.T) string {
	t.Helper()
	tmpBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmpBase)
	xdg.Reload()
	t.Cleanup(xdg.Reload)
	return tmpBase
}

//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
func Test_EnsureDefaultGroupExistsConcurrent(t *testing.T) {
	stateHome := setupIsolatedState(t)

	const goroutines = 8
	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			errCh <- ensureDefaultGroupExists(context.Background())
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("timed out waiting for concurrent ensureDefaultGroupExists calls")
	}

	for range goroutines {
		select {
		case err := <-errCh:
			assert.NoError(t, err)
		default:
			t.Fatal("not all goroutines reported a result")
		}
	}

	groupPath := filepath.Join(stateHome, "toolhive", "groups", string(groups.DefaultGroupName)+".json")
	data, err := os.ReadFile(groupPath) //nolint:gosec // path is built from a test-controlled temp dir
	require.NoError(t, err, "default group file should exist and be readable")

	var group groups.Group
	require.NoError(t, json.Unmarshal(data, &group), "group file should contain complete valid JSON")
	assert.Equal(t, string(groups.DefaultGroupName), group.Name)
}

//nolint:paralleltest // t.Setenv is incompatible with t.Parallel
func Test_EnsureDefaultGroupExistsIdempotent(t *testing.T) {
	setupIsolatedState(t)

	require.NoError(t, ensureDefaultGroupExists(context.Background()))
	require.NoError(t, ensureDefaultGroupExists(context.Background()))
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package projecttxn

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock_CanceledWaiterDoesNotAcquire(t *testing.T) {
	t.Parallel()

	projectRoot := t.TempDir()
	unlock, err := Lock(t.Context(), projectRoot)
	require.NoError(t, err)
	var unlockOnce sync.Once
	release := func() { unlockOnce.Do(unlock) }
	t.Cleanup(release)

	waitCtx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, lockErr := Lock(waitCtx, projectRoot)
		done <- lockErr
	}()
	cancel()

	select {
	case lockErr := <-done:
		require.ErrorIs(t, lockErr, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled project transaction waiter did not return")
	}

	release()
	reacquired, err := Lock(t.Context(), projectRoot)
	require.NoError(t, err, "a canceled waiter must not consume the lock token")
	reacquired()
}

func TestRun_CanceledWaiterDoesNotInvokeCallback(t *testing.T) {
	t.Parallel()

	projectRoot := t.TempDir()
	unlock, err := Lock(t.Context(), projectRoot)
	require.NoError(t, err)
	var unlockOnce sync.Once
	release := func() { unlockOnce.Do(unlock) }
	t.Cleanup(release)

	waitCtx, cancel := context.WithCancel(t.Context())
	entered := atomic.Bool{}
	done := make(chan error, 1)
	go func() {
		done <- Run(waitCtx, projectRoot, func() error {
			entered.Store(true)
			return nil
		})
	}()
	cancel()

	select {
	case runErr := <-done:
		require.ErrorIs(t, runErr, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled project transaction did not return")
	}
	release()
	assert.False(t, entered.Load(), "a canceled transaction must never run its callback")
}

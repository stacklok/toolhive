// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWaitForExit_AlreadyExited(t *testing.T) {
	t.Parallel()

	// Use a PID that does not exist - FindProcess returns false immediately
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := WaitForExit(ctx, 999999999)
	require.NoError(t, err)
}

func TestWaitForExit_ContextCancelled(t *testing.T) {
	t.Parallel()

	// Use our own PID - process is running, so WaitForExit will loop
	// Cancel context immediately so we exit with context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := WaitForExit(ctx, os.Getpid())
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

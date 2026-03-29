// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKillProcess_InvalidPID(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("pid_%d", pid), func(t *testing.T) {
			t.Parallel()
			err := KillProcess(pid)
			require.Error(t, err, "KillProcess(%d) should return an error", pid)
			assert.Contains(t, err.Error(), "invalid PID")
		})
	}
}

func TestFindProcess_InvalidPID(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("pid_%d", pid), func(t *testing.T) {
			t.Parallel()
			alive, err := FindProcess(pid)
			require.Error(t, err, "FindProcess(%d) should return an error", pid)
			assert.False(t, alive, "FindProcess(%d) should return false", pid)
			assert.Contains(t, err.Error(), "invalid PID")
		})
	}
}

func TestWaitForExit_InvalidPID(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1, -100} {
		t.Run(fmt.Sprintf("pid_%d", pid), func(t *testing.T) {
			t.Parallel()
			err := WaitForExit(context.Background(), pid)
			require.Error(t, err, "WaitForExit(%d) should return an error", pid)
			assert.Contains(t, err.Error(), "invalid PID")
		})
	}
}

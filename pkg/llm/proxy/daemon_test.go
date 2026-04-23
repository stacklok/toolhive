// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package proxy

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsRunning_NoPIDFile(t *testing.T) {
	t.Parallel()
	pidPath := filepath.Join(t.TempDir(), "llm-proxy.pid")
	running, pid, err := isRunningAt(pidPath)
	require.NoError(t, err)
	assert.False(t, running)
	assert.Zero(t, pid)
}

func TestIsRunning_DeadProcess(t *testing.T) {
	t.Parallel()
	pidPath := filepath.Join(t.TempDir(), "llm-proxy.pid")
	require.NoError(t, os.WriteFile(pidPath, []byte("99999999"), 0600))
	running, _, err := isRunningAt(pidPath)
	require.NoError(t, err)
	assert.False(t, running)
}

func TestStop_WhenNotRunning(t *testing.T) {
	t.Parallel()
	pidPath := filepath.Join(t.TempDir(), "llm-proxy.pid")
	err := stopAt(pidPath)
	assert.ErrorContains(t, err, "not running")
}

func TestStartDetached_RejectsWhenAlreadyRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "llm-proxy.pid")
	logPath := filepath.Join(dir, "llm-proxy.log")
	// Write our own PID — current process is definitely alive.
	require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600))

	_, err := startDetachedAt(pidPath, logPath, "/bin/true", false)
	assert.ErrorContains(t, err, "already running")
}

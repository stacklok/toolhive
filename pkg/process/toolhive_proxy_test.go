// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsToolHiveProxyForWorkload_InvalidPID(t *testing.T) {
	t.Parallel()
	isToolHive, err := IsToolHiveProxyForWorkload(0, "")
	require.NoError(t, err)
	assert.False(t, isToolHive)

	isToolHive, err = IsToolHiveProxyForWorkload(-1, "")
	require.NoError(t, err)
	assert.False(t, isToolHive)
}

func TestIsToolHiveProxyForWorkload_NonToolHiveProcess(t *testing.T) {
	t.Parallel()
	// Use a very high PID that almost certainly doesn't exist.
	// IsToolHiveProxyForWorkload should return false (fail-safe: don't kill unknown processes).
	isToolHive, err := IsToolHiveProxyForWorkload(999999999, "")
	if err != nil {
		// Process may not exist; either way we must not report it as ToolHive
		assert.False(t, isToolHive)
		return
	}
	require.NoError(t, err)
	assert.False(t, isToolHive)
}

func TestContainsThvBinary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"thv exe", "thv.exe", true},
		{"path with thv", "/usr/local/bin/thv", true},
		{"path with thv windows", `C:\tools\thv.exe`, true},
		{"cmd thv start", "thv start myworkload --foreground", true},
		{"cmd with thv in middle", "/path/to/thv start x", true},
		{"toolhive not thv", "toolhive", false},
		{"toolhive.test", "/tmp/toolhive.test", false},
		{"unrelated", "node server.js", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := containsThvBinary(tt.input)
			assert.Equal(t, tt.expected, got, "containsThvBinary(%q)", tt.input)
		})
	}
}

func TestCmdlineContainsWorkload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cmdline  string
		workload string
		expected bool
	}{
		{"matches github", "/usr/bin/thv start github --foreground", "github", true},
		{"matches with debug", "thv start slack --foreground --debug", "slack", true},
		{"matches at end", "thv start myworkload", "myworkload", true},
		{"rejects different workload", "/usr/bin/thv start slack --foreground", "github", false},
		{"rejects partial match", "thv start github --foreground", "git", false},
		{"rejects suffix match", "thv start github --foreground", "hub", false},
		{"empty workload", "thv start github --foreground", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cmdlineContainsWorkload(tt.cmdline, tt.workload)
			assert.Equal(t, tt.expected, got, "cmdlineContainsWorkload(%q, %q)", tt.cmdline, tt.workload)
		})
	}
}

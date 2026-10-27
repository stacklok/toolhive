// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestIsTrackedToolHiveProxyForWorkload_DetachedRequiresExactWorkload(t *testing.T) {
	t.Parallel()
	pid := startIdentityProcess(t, true, "start", "github-extra", "--foreground")

	matched, err := IsTrackedToolHiveProxyForWorkload(pid, "github-extra")
	require.NoError(t, err)
	assert.True(t, matched, "detached supervisor must match its complete workload name")
	matched, err = IsTrackedToolHiveProxyForWorkload(pid, "github")
	require.NoError(t, err)
	assert.False(t, matched, "a hyphen continues the workload name, not a token boundary")
}

func TestIsTrackedToolHiveProxyForWorkload_ForegroundDoesNotRelaxPortIdentity(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "unnamed run", args: []string{"run", "--foreground", "example/server"}},
		{name: "named run", args: []string{"run", "--foreground", "--name", "derived-workload-name", "example/server"}},
		{name: "start", args: []string{"start", "derived-workload-name", "--foreground"}},
		{name: "restart", args: []string{"restart", "derived-workload-name", "--foreground"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pid := startIdentityProcess(t, false, tt.args...)
			matched, err := IsTrackedToolHiveProxyForWorkload(pid, "derived-workload-name")
			require.NoError(t, err)
			assert.True(t, matched, "tracked foreground supervisors must remain stoppable")
			if tt.args[0] != "start" {
				matched, err = IsToolHiveProxyForWorkload(pid, "derived-workload-name")
				require.NoError(t, err)
				assert.False(t, matched, "port-discovered foreground supervisor still requires start-workload identity")
			}
		})
	}
}

func TestIsTrackedToolHiveProxyForWorkload_RejectsForegroundServe(t *testing.T) {
	t.Parallel()
	pid := startIdentityProcess(t, false, "serve")

	toolHive, err := IsToolHiveProxyForWorkload(pid, "")
	require.NoError(t, err)
	require.True(t, toolHive, "fixture must have ToolHive binary identity without a detached marker")
	matched, err := IsTrackedToolHiveProxyForWorkload(pid, "stale-workload")
	require.NoError(t, err)
	assert.False(t, matched, "a stale supervisor PID must not identify ToolHive serve as a workload supervisor")
}

func TestToolHiveIdentityProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv("TOOLHIVE_TEST_IDENTITY_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	<-time.After(time.Minute)
	os.Exit(1)
}

func startIdentityProcess(t *testing.T, detached bool, args ...string) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("process identity fixture requires Unix symlinks and process inspection")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	if !detached {
		thv := filepath.Join(t.TempDir(), "thv")
		require.NoError(t, os.Symlink(executable, thv))
		executable = thv
	}
	cmd := exec.Command(executable, append([]string{"-test.run=^TestToolHiveIdentityProcess$", "--"}, args...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, ToolHiveDetachedEnv+"=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "TOOLHIVE_TEST_IDENTITY_HELPER=1")
	if detached {
		cmd.Env = append(cmd.Env, ToolHiveDetachedEnv+"="+ToolHiveDetachedValue)
	}
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("identity helper was not reaped")
		}
	})
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		require.Equal(t, "ready\n", line)
	case <-time.After(5 * time.Second):
		t.Fatal("identity helper did not start")
	}
	return cmd.Process.Pid
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
		{"rejects hyphenated name prefix", "thv start github-extra --foreground", "github", false},
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

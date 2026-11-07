// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"bufio"
	"errors"
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
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/process/mocks"
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

func TestIsTrackedToolHiveProxyForWorkload_RejectsMissingIdentity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		pid      int
		workload string
	}{
		{name: "zero PID", pid: 0, workload: "github"},
		{name: "negative PID", pid: -1, workload: "github"},
		{name: "empty workload", pid: os.Getpid()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			matched, err := IsTrackedToolHiveProxyForWorkload(tt.pid, tt.workload)
			require.NoError(t, err)
			assert.False(t, matched, "both a valid PID and a workload name are required")
		})
	}
}

//nolint:tparallel // owned-child cleanup must finish before the parent looks up its reaped PID
func TestIsTrackedToolHiveProxyForWorkload_ReapedProcess(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("process identity fixture requires Unix symlinks and process inspection")
	}
	var pid int
	t.Run("owned child", func(t *testing.T) {
		pid = startIdentityProcess(t, true, "start", "github", "--foreground")
	})
	require.Positive(t, pid)
	// The subtest's cleanup kills and reaps the child before looking up its PID.
	matched, err := IsTrackedToolHiveProxyForWorkload(pid, "github")
	require.Error(t, err)
	assert.False(t, matched, "a stale PID must not identify a workload supervisor")
}

func TestIsTrackedToolHiveProxyForWorkload_MetadataErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		env        []string
		environErr bool
	}{
		{
			name:       "environment unavailable",
			env:        []string{ToolHiveDetachedEnv + "=" + ToolHiveDetachedValue},
			environErr: true,
		},
		{
			name: "detached command line unavailable",
			env:  []string{ToolHiveDetachedEnv + "=" + ToolHiveDetachedValue},
		},
		{name: "foreground command line unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metadata := mocks.NewMockProcessMetadata(gomock.NewController(t))
			metadataErr := errors.New("process metadata unavailable")
			if tt.environErr {
				metadata.EXPECT().Environ().Return(tt.env, metadataErr)
			} else {
				metadata.EXPECT().Environ().Return(tt.env, nil)
				if len(tt.env) > 0 {
					metadata.EXPECT().Cmdline().Return("/usr/bin/thv start github --foreground", metadataErr)
				} else {
					metadata.EXPECT().CmdlineSlice().Return(nil, metadataErr)
				}
			}

			matched, err := isTrackedToolHiveProxyForWorkload(metadata, "github")
			require.ErrorIs(t, err, metadataErr)
			assert.False(t, matched, "unreadable identity must not authorize stopping a process")
		})
	}
}

func TestIsTrackedToolHiveProxyForWorkload_ForegroundArguments(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		args     []string
		expected bool
	}{
		{name: "start another workload", args: []string{"thv", "start", "other", "--foreground"}},
		{name: "root flag before run", args: []string{"thv", "--debug", "run", "img", "--name", "demo"}, expected: true},
		{name: "equals name", args: []string{"thv", "run", "img", "--name=demo"}, expected: true},
		{name: "different name", args: []string{"thv", "run", "img", "--name", "other"}},
		{name: "different equals name", args: []string{"thv", "run", "demo", "--name=other"}},
		{name: "missing name value", args: []string{"thv", "run", "img", "--name"}},
		{name: "empty name value", args: []string{"thv", "run", "img", "--name="}},
		{name: "unnamed run", args: []string{"thv", "run", "img"}, expected: true},
		{name: "positional workload", args: []string{"thv", "run", "demo"}, expected: true},
		{name: "run without image", args: []string{"thv", "run"}},
		{name: "start all", args: []string{"thv", "start", "--all", "--foreground"}, expected: true},
		{name: "start group", args: []string{"thv", "start", "--group", "servers"}, expected: true},
		{name: "restart equals group", args: []string{"thv", "restart", "--group=servers"}, expected: true},
		{name: "restart workload", args: []string{"thv", "restart", "demo", "--foreground"}, expected: true},
		{name: "reject partial workload", args: []string{"thv", "start", "demo-extra", "--foreground"}},
		{name: "reject subcommand substring", args: []string{"thv", "serve", "--description= run img"}},
		{name: "skip executable", args: []string{"run", "demo"}},
		{name: "first subcommand wins", args: []string{"thv", "start", "other", "run", "img"}},
		{name: "serve flag value is not a subcommand", args: []string{"thv", "serve", "--sentry-environment", "run", "--port", "8080"}},
		{name: "server name does not override workload", args: []string{"thv", "run", "--foreground", "--name", "demo", "img", "--", "--name", "backend"}, expected: true},
		{name: "server name does not select workload", args: []string{"thv", "run", "--foreground", "--name", "backend", "img", "--", "--name", "demo"}},
		{name: "unnamed run with server name", args: []string{"thv", "run", "img", "--", "--name", "other"}, expected: true},
		{name: "start shorthand all", args: []string{"thv", "start", "-a", "-f"}, expected: true},
		{name: "restart shorthand cluster", args: []string{"thv", "restart", "-af"}, expected: true},
		{name: "start shorthand group", args: []string{"thv", "start", "-g", "servers", "-f"}, expected: true},
		{name: "restart shorthand equals group", args: []string{"thv", "restart", "-g=servers"}, expected: true},
		{name: "start explicit all true", args: []string{"thv", "start", "--all=true"}, expected: true},
		{name: "start explicit all false", args: []string{"thv", "start", "--all=false", "other"}},
		{name: "run config equals", args: []string{"thv", "run", "--foreground", "--from-config=demo.json"}, expected: true},
		{name: "run config argument", args: []string{"thv", "run", "--foreground", "--from-config", "demo.json"}, expected: true},
		{name: "separator before command", args: []string{"thv", "--", "run", "img"}},
		{name: "empty argv"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metadata := mocks.NewMockProcessMetadata(gomock.NewController(t))
			metadata.EXPECT().Environ().Return(nil, nil).Times(2)
			metadata.EXPECT().CmdlineSlice().Return(tt.args, nil)
			metadata.EXPECT().Exe().Return("/usr/bin/thv", nil)

			matched, err := isTrackedToolHiveProxyForWorkload(metadata, "demo")
			require.NoError(t, err)
			assert.Equal(t, tt.expected, matched)
		})
	}
}

func TestIsForegroundRunSupervisorForWorkload(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		detached bool
		args     []string
		expected bool
	}{
		{name: "detached run", detached: true, args: []string{"thv", "run", "--name", "demo", "img"}},
		{name: "foreground run", args: []string{"thv", "run", "--name", "demo", "img"}, expected: true},
		{name: "foreground start", args: []string{"thv", "start", "demo", "--foreground"}},
		{name: "different workload", args: []string{"thv", "run", "--name", "other", "img"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			metadata := mocks.NewMockProcessMetadata(gomock.NewController(t))
			if tt.detached {
				metadata.EXPECT().Environ().Return([]string{ToolHiveDetachedEnv + "=" + ToolHiveDetachedValue}, nil)
			} else {
				metadata.EXPECT().Environ().Return(nil, nil).Times(2)
				metadata.EXPECT().CmdlineSlice().Return(tt.args, nil)
				metadata.EXPECT().Exe().Return("/usr/bin/thv", nil)
			}
			matched, err := isForegroundRunSupervisorForWorkload(metadata, "demo")
			require.NoError(t, err)
			assert.Equal(t, tt.expected, matched)
		})
	}
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
	// Test flag parsing stops at the first non-flag argument, so the ToolHive
	// subcommand can follow directly without a "--" that would hide it.
	cmd := exec.Command(executable, append([]string{"-test.run=^TestToolHiveIdentityProcess$"}, args...)...)
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

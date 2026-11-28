// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandError_OmitsArgumentsAndSecrets(t *testing.T) {
	t.Parallel()

	secretValue := "ghp_superSecretToken12345"
	cmdErr := &CommandError{
		Command: containerBinary,
		Args: []string{
			"run", "-d",
			"--name", "my-mcp",
			"-e", "GITHUB_TOKEN=" + secretValue,
			"--env=API_KEY=" + secretValue,
			"--", "ghcr.io/example/mcp:latest", "--secret", secretValue,
		},
		Stderr: "Error: container failed to start\n",
		Err:    errors.New("exit status 1"),
	}

	msg := cmdErr.Error()
	assert.Equal(t, "container run failed: Error: container failed to start: exit status 1", msg)
	assert.NotContains(t, msg, secretValue)
	assert.NotContains(t, msg, "GITHUB_TOKEN")
	assert.NotContains(t, msg, "API_KEY")
}

func TestCommandError_SummaryFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *CommandError
		want string
	}{
		{
			name: "two-level subcommand with stderr",
			err: &CommandError{
				Command: containerBinary,
				Args:    []string{"network", "delete", "toolhive-app-internal"},
				Stderr:  "Error: failed to delete one or more networks",
				Err:     errors.New("exit status 1"),
			},
			want: "container network delete failed: Error: failed to delete one or more networks: exit status 1",
		},
		{
			name: "flag-only invocation omits flags",
			err: &CommandError{
				Command: containerBinary,
				Args:    []string{"--version"},
				Err:     errors.New("exit status 1"),
			},
			want: "container failed: exit status 1",
		},
		{
			name: "non-container command omits args",
			err: &CommandError{
				Command: swVersBinary,
				Args:    []string{"-productVersion"},
				Err:     errors.New("exit status 1"),
			},
			want: "sw_vers failed: exit status 1",
		},
		{
			name: "empty command falls back cleanly",
			err: &CommandError{
				Stderr: "boom",
				Err:    errors.New("exit status 1"),
			},
			want: "command failed: boom: exit status 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.err.Error())
		})
	}
}

func TestRedactSensitiveArgs(t *testing.T) {
	t.Parallel()

	input := []string{
		"run", "-d",
		"-e", "GITHUB_TOKEN=secret1",
		"--env", "API_KEY=secret2",
		"-e=INLINE_SHORT=secret3",
		"--env=INLINE_LONG=secret4",
		"-e", "INHERITED_ONLY",
		"--", "ghcr.io/example/mcp:latest", "--token", "secret5",
	}

	got := redactSensitiveArgs(input)
	assert.Equal(t, []string{
		"run", "-d",
		"-e", "GITHUB_TOKEN=[REDACTED]",
		"--env", "API_KEY=[REDACTED]",
		"-e=INLINE_SHORT=[REDACTED]",
		"--env=INLINE_LONG=[REDACTED]",
		"-e", "INHERITED_ONLY",
		"--", "ghcr.io/example/mcp:latest", "[REDACTED]", "[REDACTED]",
	}, got)
	// Verify caller slice was not mutated.
	assert.Equal(t, "GITHUB_TOKEN=secret1", input[3])
}

func TestBuildAllowedCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("accepts container binary", func(t *testing.T) {
		t.Parallel()
		cmd, err := buildAllowedCommand(ctx, containerBinary, "list", "--all")
		require.NoError(t, err)
		require.NotNil(t, cmd)
		assert.Equal(t, []string{containerBinary, "list", "--all"}, cmd.Args)
	})

	t.Run("accepts sw_vers binary", func(t *testing.T) {
		t.Parallel()
		cmd, err := buildAllowedCommand(ctx, swVersBinary, "-productVersion")
		require.NoError(t, err)
		require.NotNil(t, cmd)
		assert.Equal(t, []string{swVersBinary, "-productVersion"}, cmd.Args)
	})

	disallowedCases := []struct {
		name   string
		binary string
	}{
		{name: "empty", binary: ""},
		{name: "sh", binary: "sh"},
		{name: "absolute_sh", binary: "/bin/sh"},
		{name: "docker", binary: "docker"},
		{name: "rm", binary: "rm"},
	}
	for _, tc := range disallowedCases {
		t.Run("rejects_"+tc.name, func(t *testing.T) {
			t.Parallel()
			cmd, err := buildAllowedCommand(ctx, tc.binary, "--help")
			require.Error(t, err)
			assert.Nil(t, cmd)
			assert.Contains(t, err.Error(), "unsupported command")
		})
	}
}

func TestExecCommandRunner_RejectsDisallowedBinary(t *testing.T) {
	t.Parallel()

	runner := execCommandRunner{}
	ctx := context.Background()

	_, err := runner.Run(ctx, "sh", "-c", "echo hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported command")

	_, err = runner.StartAttached(ctx, "/bin/sh", "-c", "echo hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported command")
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package sdk

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

func makeTempSocket(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.sock")
	require.NoError(t, os.WriteFile(p, nil, 0600))
	return p
}

func TestFindContainerSocket_ConfigPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		makePath func(*testing.T) string
		wantErr  bool
	}{
		{
			name:     "valid path is used",
			makePath: makeTempSocket,
			wantErr:  false,
		},
		{
			name: "nonexistent path returns error",
			makePath: func(t *testing.T) string {
				t.Helper()
				return filepath.Join(t.TempDir(), "nonexistent.sock")
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			socketPath := tc.makePath(t)

			_, _, err := findContainerSocket(runtime.TypeDocker, runtime.SocketConfig{DockerSocket: socketPath})

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFindContainerSocket_EnvVarPrecedence(t *testing.T) {
	// not parallel — t.Setenv cannot be called in parallel tests
	envPath := makeTempSocket(t)
	t.Setenv(DockerSocketEnv, envPath)

	gotPath, gotRuntime, err := findContainerSocket(runtime.TypeDocker, runtime.SocketConfig{DockerSocket: makeTempSocket(t)})

	require.NoError(t, err)
	require.Equal(t, envPath, gotPath)
	require.Equal(t, runtime.TypeDocker, gotRuntime)
}

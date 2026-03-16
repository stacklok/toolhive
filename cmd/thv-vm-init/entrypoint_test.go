// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadEntrypoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantCmd []string
		wantErr string
	}{
		{
			name:    "valid full config",
			content: `{"cmd":["/usr/bin/mcp-server","--port","8080"],"env":["FOO=bar"],"working_dir":"/app"}`,
			wantCmd: []string{"/usr/bin/mcp-server", "--port", "8080"},
		},
		{
			name:    "valid minimal config",
			content: `{"cmd":["python3","server.py"]}`,
			wantCmd: []string{"python3", "server.py"},
		},
		{
			name:    "empty cmd",
			content: `{"cmd":[]}`,
			wantErr: "entrypoint config has empty cmd",
		},
		{
			name:    "null cmd",
			content: `{"cmd":null}`,
			wantErr: "entrypoint config has empty cmd",
		},
		{
			name:    "invalid JSON",
			content: `{not valid json}`,
			wantErr: "parsing entrypoint config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "entrypoint.json")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o644))

			cfg, err := loadEntrypoint(path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantCmd, cfg.Cmd)
		})
	}
}

func TestLoadEntrypoint_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := loadEntrypoint("/nonexistent/path/entrypoint.json")
	require.ErrorContains(t, err, "reading entrypoint config")
}

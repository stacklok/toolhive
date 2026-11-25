// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package identitytoken

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	tokenFile := filepath.Join(resolved, "token.jwt")
	require.NoError(t, os.WriteFile(tokenFile, []byte("  eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig\n"), 0o600))

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr string
	}{
		{
			name:  "readable file wins, trimmed",
			value: tokenFile,
			want:  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig",
		},
		{
			name:  "JWT-shaped literal",
			value: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig",
			want:  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.sig",
		},
		{
			name:    "neither a file nor JWT-shaped",
			value:   filepath.Join(resolved, "does-not-exist.jwt"),
			wantErr: "neither a readable file nor a JWT",
		},
		{
			name:    "empty value",
			value:   "",
			wantErr: "neither a readable file nor a JWT",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Resolve(tc.value)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

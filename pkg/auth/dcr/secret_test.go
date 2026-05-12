// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveSecret pins the dcr-package copy of resolveSecret to the same
// observable contract as the parallel runner-package copy
// (pkg/authserver/runner/embeddedauthserver_test.go::TestResolveSecret*).
// Two physically-distinct copies of this helper exist by design (the dcr
// package must not reach back into pkg/authserver/runner per its
// profile-agnostic charter); this test guards against silent drift between
// them. If a future bug fix lands on one copy without being mirrored to the
// other, this test or its runner-package twin will fail.
//
// Cases that take t.Setenv() are kept out of the parallel sub-suite because
// t.Setenv requires a non-parallel test scope.
func TestResolveSecret(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("  secret-value  \n"), 0o600))

	cases := []struct {
		name        string
		file        string
		envVar      string
		want        string
		wantErr     bool
		wantErrSubs []string
	}{
		{
			name: "neither file nor env var set returns empty string and no error",
			file: "", envVar: "",
			want: "",
		},
		{
			name: "file content is read and surrounding whitespace trimmed",
			file: secretFile, envVar: "",
			want: "secret-value",
		},
		{
			name: "missing file returns wrapped read error",
			file: "/nonexistent/file", envVar: "",
			wantErr: true, wantErrSubs: []string{"failed to read secret file"},
		},
		{
			name: "env var name is set but env var is empty returns explanatory error",
			// Use a unique env var name that won't be set in the environment.
			file: "", envVar: "TEST_SECRET_NOT_SET_DCR_PKG_12345",
			wantErr: true, wantErrSubs: []string{"environment variable", "is not set"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveSecret(tc.file, tc.envVar)
			if tc.wantErr {
				require.Error(t, err)
				for _, sub := range tc.wantErrSubs {
					assert.Contains(t, err.Error(), sub)
				}
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveSecretWithEnvVar covers the env-var paths separately because
// t.Setenv requires a non-parallel test scope. Mirrors the runner-package
// twin (TestResolveSecretWithEnvVar in embeddedauthserver_test.go).
func TestResolveSecretWithEnvVar(t *testing.T) {
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("secret-from-file"), 0o600))

	t.Run("file takes precedence over env var when both are set", func(t *testing.T) {
		envVar := "TEST_SECRET_FILE_PRECEDENCE_DCR_PKG"
		t.Setenv(envVar, "secret-from-env")

		got, err := resolveSecret(secretFile, envVar)
		require.NoError(t, err)
		assert.Equal(t, "secret-from-file", got)
	})

	t.Run("env var is read when file is empty", func(t *testing.T) {
		envVar := "TEST_SECRET_ENV_ONLY_DCR_PKG"
		t.Setenv(envVar, "secret-from-env")

		got, err := resolveSecret("", envVar)
		require.NoError(t, err)
		assert.Equal(t, "secret-from-env", got)
	})

	t.Run("missing file does not silently fall back to env var", func(t *testing.T) {
		envVar := "TEST_SECRET_NO_FALLBACK_DCR_PKG"
		t.Setenv(envVar, "secret-from-env")

		got, err := resolveSecret("/nonexistent/file", envVar)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read secret file")
		assert.Empty(t, got)
	})
}

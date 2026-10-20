// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testCLIKeyDERBase64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExlVDpbnOEv2fH3gS8n7UCHS9Gs0wKxIPR5EAcl8F1jSxlxAV/pll0NsSiuAK95Ws4Fpkn+5QkdVKNXy7LHgb2A=="

// TestResolveProjectRootAbsolutizesExplicit covers the sync/upgrade path,
// where an explicit value short-circuits auto-detection — it must still be
// made absolute, which is the regression behind #6211.
//
//nolint:paralleltest // uses t.Chdir, incompatible with t.Parallel
func TestResolveProjectRootAbsolutizesExplicit(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	t.Chdir(resolved)

	got, err := resolveProjectRoot(".")
	require.NoError(t, err)
	assert.Equal(t, resolved, got)
	assert.True(t, filepath.IsAbs(got), "an explicit --project-root must reach the API server absolute")
}

func TestReadInstallPublicKey(t *testing.T) {
	t.Parallel()

	der, err := base64.StdEncoding.DecodeString(testCLIKeyDERBase64)
	require.NoError(t, err)
	validPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	require.NotEmpty(t, validPEM)

	tests := []struct {
		name    string
		content []byte
		missing bool
		want    string
		wantErr string
	}{
		{name: "empty path is omitted"},
		{name: "valid PEM is encoded for the API", content: validPEM, want: testCLIKeyDERBase64},
		{name: "malformed key is rejected", content: []byte("not a public key"), wantErr: "read public key"},
		{name: "missing file is reported", missing: true, wantErr: "read public key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := ""
			if tc.content != nil {
				path = filepath.Join(t.TempDir(), "cosign.pub")
				require.NoError(t, os.WriteFile(path, tc.content, 0o600))
			} else if tc.missing {
				path = filepath.Join(t.TempDir(), "missing.pub")
			}

			got, err := readInstallPublicKey(path)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSkillReanchorFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		present bool
	}{
		{name: "sync public-key", present: skillSyncCmd.Flags().Lookup("public-key") != nil},
		{name: "upgrade public-key", present: skillUpgradeCmd.Flags().Lookup("public-key") != nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, tc.present)
		})
	}
}

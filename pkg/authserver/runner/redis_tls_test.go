// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

func TestConvertRedisTLSRunConfig(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns nil", func(t *testing.T) {
		t.Parallel()
		result := convertRedisTLSRunConfig(nil)
		assert.Nil(t, result)
	})

	t.Run("basic enabled config", func(t *testing.T) {
		t.Parallel()
		rc := &storage.RedisTLSRunConfig{
			Enabled: true,
		}
		result := convertRedisTLSRunConfig(rc)
		require.NotNil(t, result)
		assert.True(t, result.Enabled)
		assert.False(t, result.InsecureSkipVerify)
		assert.Empty(t, result.CACert)
	})

	t.Run("insecure skip verify", func(t *testing.T) {
		t.Parallel()
		rc := &storage.RedisTLSRunConfig{
			Enabled:            true,
			InsecureSkipVerify: true,
		}
		result := convertRedisTLSRunConfig(rc)
		require.NotNil(t, result)
		assert.True(t, result.InsecureSkipVerify)
	})

	t.Run("CA cert file is read", func(t *testing.T) {
		t.Parallel()

		// Write a test CA cert file
		dir := t.TempDir()
		certPath := filepath.Join(dir, "ca.crt")
		certData := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
		require.NoError(t, os.WriteFile(certPath, certData, 0600))

		rc := &storage.RedisTLSRunConfig{
			Enabled:    true,
			CACertFile: certPath,
		}
		result := convertRedisTLSRunConfig(rc)
		require.NotNil(t, result)
		assert.Equal(t, certData, result.CACert)
	})

	t.Run("missing CA cert file logs warning and uses empty cert", func(t *testing.T) {
		t.Parallel()
		rc := &storage.RedisTLSRunConfig{
			Enabled:    true,
			CACertFile: "/nonexistent/ca.crt",
		}
		result := convertRedisTLSRunConfig(rc)
		require.NotNil(t, result)
		assert.Empty(t, result.CACert, "missing cert file should result in empty CACert")
	})
}

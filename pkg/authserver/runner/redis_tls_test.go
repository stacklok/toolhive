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
		result, err := convertRedisTLSRunConfig(nil)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("empty config enables TLS with defaults", func(t *testing.T) {
		t.Parallel()
		rc := &storage.RedisTLSRunConfig{}
		result, err := convertRedisTLSRunConfig(rc)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.InsecureSkipVerify)
		assert.Empty(t, result.CACert)
	})

	t.Run("insecure skip verify", func(t *testing.T) {
		t.Parallel()
		rc := &storage.RedisTLSRunConfig{
			InsecureSkipVerify: true,
		}
		result, err := convertRedisTLSRunConfig(rc)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.InsecureSkipVerify)
	})

	t.Run("CA cert file is read", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		certPath := filepath.Join(dir, "ca.crt")
		certData := []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n")
		require.NoError(t, os.WriteFile(certPath, certData, 0600))

		rc := &storage.RedisTLSRunConfig{
			CACertFile: certPath,
		}
		result, err := convertRedisTLSRunConfig(rc)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, certData, result.CACert)
	})

	t.Run("missing CA cert file returns error", func(t *testing.T) {
		t.Parallel()
		rc := &storage.RedisTLSRunConfig{
			CACertFile: "/nonexistent/ca.crt",
		}
		_, err := convertRedisTLSRunConfig(rc)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read Redis CA cert file")
	})
}

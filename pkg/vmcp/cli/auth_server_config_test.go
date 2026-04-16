// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	authserverconfig "github.com/stacklok/toolhive/pkg/authserver"
)

func TestLoadAuthServerConfig(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when file does not exist", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		configPath := filepath.Join(dir, "vmcp-config.yaml")

		rc, err := loadAuthServerConfig(configPath)

		require.NoError(t, err)
		assert.Nil(t, rc)
	})

	t.Run("returns populated RunConfig for valid YAML", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		configPath := filepath.Join(dir, "vmcp-config.yaml")

		want := &authserverconfig.RunConfig{
			Issuer:        "https://test-issuer.example.com",
			SchemaVersion: "1",
		}

		data, err := yaml.Marshal(want)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "authserver-config.yaml"),
			data,
			0o600,
		))

		rc, err := loadAuthServerConfig(configPath)

		require.NoError(t, err)
		require.NotNil(t, rc)
		assert.Equal(t, "https://test-issuer.example.com", rc.Issuer)
		assert.Equal(t, "1", rc.SchemaVersion)
	})

	t.Run("returns error for invalid YAML", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		configPath := filepath.Join(dir, "vmcp-config.yaml")

		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "authserver-config.yaml"),
			[]byte(":::not valid yaml"),
			0o600,
		))

		rc, err := loadAuthServerConfig(configPath)

		require.Error(t, err)
		assert.Nil(t, rc)
		assert.Contains(t, err.Error(), "failed to parse")
	})
}

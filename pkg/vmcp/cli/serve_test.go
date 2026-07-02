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
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestLoadAndValidateConfig covers all config-loading paths.
func TestLoadAndValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		wantErr     bool
		errContains string
	}{
		{
			name:    "valid config",
			content: validConfigYAML,
			wantErr: false,
		},
		{
			name:        "non-existent file",
			content:     "", // file will not be created
			wantErr:     true,
			errContains: "configuration loading failed",
		},
		{
			name:        "malformed YAML",
			content:     ":::invalid yaml:::",
			wantErr:     true,
			errContains: "configuration loading failed",
		},
		{
			name: "fails semantic validation — missing groupRef",
			content: `
name: test-vmcp
incomingAuth:
  type: anonymous
outgoingAuth:
  source: inline
aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"
`,
			wantErr:     true,
			errContains: "group reference is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "vmcp.yaml")
			if tc.content != "" {
				require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			}

			cfg, err := loadAndValidateConfig(path)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.errContains)
				require.Nil(t, cfg)
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
				assert.Equal(t, "test-group", cfg.Group)
			}
		})
	}
}

// TestLoadAuthServerConfig covers all auth-server-config side-loading paths.
// (Additional cases live in auth_server_config_test.go, moved from cmd/vmcp/app.)
func TestLoadAuthServerConfig_NestedDir(t *testing.T) {
	t.Parallel()

	// Config lives in a subdirectory; sibling authserver-config.yaml must be found correctly.
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub", "dir")
	require.NoError(t, os.MkdirAll(subdir, 0o750))
	configPath := filepath.Join(subdir, "vmcp-config.yaml")

	want := &authserverconfig.RunConfig{
		Issuer:        "https://nested.example.com",
		SchemaVersion: "1",
	}
	data, err := yaml.Marshal(want)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(subdir, "authserver-config.yaml"), data, 0o600))

	rc, err := loadAuthServerConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, rc)
	assert.Equal(t, "https://nested.example.com", rc.Issuer)
}

// TestGenerateQuickModeConfig covers the generateQuickModeConfig helper.
func TestGenerateQuickModeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupRef    string
		wantErr     bool
		errContains string
	}{
		{
			name:     "valid group sets groupRef and inline source",
			groupRef: "default",
		},
		{
			name:     "group name with hyphens",
			groupRef: "my-group",
		},
		{
			name:        "empty groupRef returns error",
			groupRef:    "",
			wantErr:     true,
			errContains: "--group must not be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := generateQuickModeConfig(tc.groupRef)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.errContains)
				require.Nil(t, cfg)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, cfg)
			require.Equal(t, tc.groupRef, cfg.Group)
			require.NotNil(t, cfg.OutgoingAuth)
			require.Equal(t, "inline", cfg.OutgoingAuth.Source)
			require.NotNil(t, cfg.IncomingAuth)
			require.Equal(t, "anonymous", cfg.IncomingAuth.Type)
			// Verify the generated config passes the real validator.
			require.NoError(t, vmcpconfig.NewValidator().Validate(cfg))
		})
	}
}

// TestServe_NeitherConfigNorGroup verifies that Serve returns an error when
// both --config and --group are absent.
func TestServe_NeitherConfigNorGroup(t *testing.T) {
	t.Parallel()

	err := Serve(t.Context(), ServeConfig{})
	require.Error(t, err)
	require.ErrorContains(t, err, "--config or --group")
}

// TestValidateQuickModeHost exercises ServeConfig.validateQuickModeHost directly
// so the test never starts the HTTP server and cannot hang.
func TestValidateQuickModeHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		configPath  string
		groupRef    string
		host        string
		wantErr     bool
		errContains string
	}{
		// Quick mode (no --config): loopback-only
		{name: "quick mode: loopback IPv4 allowed", groupRef: "my-group", host: "127.0.0.1"},
		{name: "quick mode: loopback IPv6 allowed", groupRef: "my-group", host: "::1"},
		{name: "quick mode: localhost allowed", groupRef: "my-group", host: "localhost"},
		{name: "quick mode: empty host treated as loopback", groupRef: "my-group", host: ""},
		{name: "quick mode: all-interfaces rejected", groupRef: "my-group", host: "0.0.0.0", wantErr: true, errContains: "quick mode"},
		{name: "quick mode: LAN IP rejected", groupRef: "my-group", host: "192.168.1.10", wantErr: true, errContains: "quick mode"},
		{name: "quick mode: non-IP hostname rejected", groupRef: "my-group", host: "not-an-ip", wantErr: true, errContains: "quick mode"},
		// Config-file mode: host check does not apply
		{name: "config mode: non-loopback allowed", configPath: "/some/config.yaml", host: "0.0.0.0"},
		// Both flags set: ConfigPath takes precedence, host check skipped
		{name: "both flags: non-loopback allowed", configPath: "/some/config.yaml", groupRef: "my-group", host: "0.0.0.0"},
		// Neither flag: check is a no-op
		{name: "neither flag: no-op", host: "0.0.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ServeConfig{ConfigPath: tc.configPath, GroupRef: tc.groupRef, Host: tc.host}.validateQuickModeHost()
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

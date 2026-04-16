// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v3"

	envmocks "github.com/stacklok/toolhive-core/env/mocks"
	authserverconfig "github.com/stacklok/toolhive/pkg/authserver"
	aggregatormocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	clientmocks "github.com/stacklok/toolhive/pkg/vmcp/client/mocks"
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
`,
			wantErr:     true,
			errContains: "validation failed",
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

// TestDiscoverBackends_StaticMode exercises the static-backend path without
// needing a live Kubernetes API.
func TestDiscoverBackends_StaticMode(t *testing.T) {
	t.Parallel()

	// Build a minimal config with one static backend.
	dir := t.TempDir()
	path := filepath.Join(dir, "vmcp.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
name: test-vmcp
groupRef: test-group

incomingAuth:
  type: anonymous

outgoingAuth:
  source: inline
  default:
    type: unauthenticated

aggregation:
  conflictResolution: prefix
  conflictResolutionConfig:
    prefixFormat: "{workload}_"

backends:
  - name: backend-one
    url: http://127.0.0.1:9001/sse
    transport: sse
`), 0o600))

	cfg, err := loadAndValidateConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.Backends, 1)

	backends, client, registry, err := discoverBackends(t.Context(), cfg)
	require.NoError(t, err)
	assert.NotNil(t, client)
	assert.NotNil(t, registry)
	// Static mode: one backend discovered.
	assert.Len(t, backends, 1)
}

func newSessionFactoryMocks(t *testing.T) (*envmocks.MockReader, *clientmocks.MockOutgoingAuthRegistry, *aggregatormocks.MockAggregator) {
	t.Helper()
	ctrl := gomock.NewController(t)
	return envmocks.NewMockReader(ctrl), clientmocks.NewMockOutgoingAuthRegistry(ctrl), aggregatormocks.NewMockAggregator(ctrl)
}

func TestCreateSessionFactory_WithHMACSecret(t *testing.T) {
	t.Parallel()
	envReader, registry, agg := newSessionFactoryMocks(t)
	envReader.EXPECT().Getenv("VMCP_SESSION_HMAC_SECRET").Return("a-sufficiently-long-hmac-secret-value-32b")
	factory, err := createSessionFactory(envReader, registry, agg)
	require.NoError(t, err)
	require.NotNil(t, factory)
}

func TestCreateSessionFactory_HMACSecretExactly32Bytes(t *testing.T) {
	t.Parallel()
	envReader, registry, agg := newSessionFactoryMocks(t)
	envReader.EXPECT().Getenv("VMCP_SESSION_HMAC_SECRET").Return("12345678901234567890123456789012")
	factory, err := createSessionFactory(envReader, registry, agg)
	require.NoError(t, err)
	require.NotNil(t, factory)
}

func TestCreateSessionFactory_ShortHMACSecret(t *testing.T) {
	t.Parallel()
	envReader, registry, agg := newSessionFactoryMocks(t)
	envReader.EXPECT().Getenv("VMCP_SESSION_HMAC_SECRET").Return("short")
	factory, err := createSessionFactory(envReader, registry, agg)
	require.NoError(t, err)
	require.NotNil(t, factory)
}

func TestCreateSessionFactory_NoSecretNonKubernetes(t *testing.T) {
	t.Parallel()
	envReader, registry, agg := newSessionFactoryMocks(t)
	envReader.EXPECT().Getenv("VMCP_SESSION_HMAC_SECRET").Return("")
	envReader.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("")
	envReader.EXPECT().Getenv("KUBERNETES_SERVICE_HOST").Return("")
	factory, err := createSessionFactory(envReader, registry, agg)
	require.NoError(t, err)
	require.NotNil(t, factory)
}

func TestCreateSessionFactory_NoSecretKubernetes(t *testing.T) {
	t.Parallel()
	envReader, registry, agg := newSessionFactoryMocks(t)
	envReader.EXPECT().Getenv("VMCP_SESSION_HMAC_SECRET").Return("")
	envReader.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("")
	envReader.EXPECT().Getenv("KUBERNETES_SERVICE_HOST").Return("10.0.0.1")
	factory, err := createSessionFactory(envReader, registry, agg)
	require.Error(t, err)
	require.ErrorContains(t, err, "VMCP_SESSION_HMAC_SECRET environment variable is required")
	require.Nil(t, factory)
}

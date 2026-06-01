// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgrade

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry"
	registrymocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

const testServerName = "example-server"

func TestNewChecker(t *testing.T) {
	t.Parallel()

	t.Run("nil provider fails", func(t *testing.T) {
		t.Parallel()
		c, err := NewChecker(nil)
		require.Error(t, err)
		assert.Nil(t, c)
	})

	t.Run("valid provider succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		c, err := NewChecker(registrymocks.NewMockProvider(ctrl))
		require.NoError(t, err)
		assert.NotNil(t, c)
	})
}

func TestChecker_Check(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        *runner.RunConfig
		setupMock  func(m *registrymocks.MockProvider)
		wantStatus UpgradeStatus
		wantReason bool // true if Reason must be non-empty
		assertMore func(t *testing.T, res *CheckResult)
	}{
		{
			name: "not registry sourced",
			cfg: &runner.RunConfig{
				Name:  "raw",
				Image: "ghcr.io/example/server:1.0.0",
			},
			setupMock:  func(_ *registrymocks.MockProvider) {},
			wantStatus: StatusNotRegistrySourced,
		},
		{
			name: "server not found",
			cfg: &runner.RunConfig{
				Name:               "wl",
				Image:              "ghcr.io/example/server:1.0.0",
				RegistryServerName: testServerName,
			},
			setupMock: func(m *registrymocks.MockProvider) {
				m.EXPECT().GetServer(testServerName).Return(nil, registry.ErrServerNotFound)
			},
			wantStatus: StatusServerNotFound,
		},
		{
			name: "registry lookup error is unknown",
			cfg: &runner.RunConfig{
				Name:               "wl",
				Image:              "ghcr.io/example/server:1.0.0",
				RegistryServerName: testServerName,
			},
			setupMock: func(m *registrymocks.MockProvider) {
				m.EXPECT().GetServer(testServerName).Return(nil, fmt.Errorf("network down"))
			},
			wantStatus: StatusUnknown,
			wantReason: true,
		},
		{
			name: "non-image entry is unknown",
			cfg: &runner.RunConfig{
				Name:               "wl",
				Image:              "ghcr.io/example/server:1.0.0",
				RegistryServerName: testServerName,
			},
			setupMock: func(m *registrymocks.MockProvider) {
				m.EXPECT().GetServer(testServerName).Return(&regtypes.RemoteServerMetadata{}, nil)
			},
			wantStatus: StatusUnknown,
			wantReason: true,
		},
		{
			name: "up to date",
			cfg: &runner.RunConfig{
				Name:               "wl",
				Image:              "ghcr.io/example/server:1.2.0",
				RegistryServerName: testServerName,
			},
			setupMock: func(m *registrymocks.MockProvider) {
				m.EXPECT().GetServer(testServerName).Return(imageMeta("ghcr.io/example/server:1.2.0"), nil)
			},
			wantStatus: StatusUpToDate,
		},
		{
			name: "undecidable tags is unknown",
			cfg: &runner.RunConfig{
				Name:               "wl",
				Image:              "ghcr.io/example/server:latest",
				RegistryServerName: testServerName,
			},
			setupMock: func(m *registrymocks.MockProvider) {
				m.EXPECT().GetServer(testServerName).Return(imageMeta("ghcr.io/example/server:1.2.0"), nil)
			},
			wantStatus: StatusUnknown,
			wantReason: true,
		},
		{
			name: "upgrade available with no drift",
			cfg: &runner.RunConfig{
				Name:               "wl",
				Image:              "ghcr.io/example/server:1.0.0",
				RegistryServerName: testServerName,
			},
			setupMock: func(m *registrymocks.MockProvider) {
				m.EXPECT().GetServer(testServerName).Return(imageMeta("ghcr.io/example/server:1.2.0"), nil)
			},
			wantStatus: StatusUpgradeAvailable,
			assertMore: func(t *testing.T, res *CheckResult) {
				t.Helper()
				assert.Equal(t, "ghcr.io/example/server:1.2.0", res.CandidateImage)
				assert.Nil(t, res.EnvVarDrift)
				assert.Nil(t, res.ConfigDrift)
			},
		},
		{
			name: "upgrade available surfaces env and config drift",
			cfg: &runner.RunConfig{
				Name:                        "wl",
				Image:                       "ghcr.io/example/server:1.0.0",
				RegistryServerName:          testServerName,
				Transport:                   transporttypes.TransportTypeStdio,
				PermissionProfileNameOrPath: "none",
				EnvVars:                     map[string]string{"LOG_LEVEL": "info"},
				Secrets:                     []string{"mykey,target=API_KEY"},
			},
			setupMock: func(m *registrymocks.MockProvider) {
				meta := imageMeta("ghcr.io/example/server:1.2.0")
				meta.Transport = string(transporttypes.TransportTypeStreamableHTTP)
				meta.Permissions = &permissions.Profile{Name: "network"}
				meta.EnvVars = []*regtypes.EnvVar{
					{Name: "LOG_LEVEL"},                                    // satisfied via EnvVars
					{Name: "API_KEY", Secret: true},                        // satisfied via secret target
					{Name: "NEW_VAR", Required: true},                      // drift: added
					{Name: "NEW_SECRET", Secret: true, Default: "leak-me"}, // drift: added, default cleared
				}
				m.EXPECT().GetServer(testServerName).Return(meta, nil)
			},
			wantStatus: StatusUpgradeAvailable,
			assertMore: func(t *testing.T, res *CheckResult) {
				t.Helper()
				require.NotNil(t, res.EnvVarDrift)
				names := make(map[string]EnvVarInfo, len(res.EnvVarDrift.Added))
				for _, e := range res.EnvVarDrift.Added {
					names[e.Name] = e
				}
				assert.Len(t, names, 2)
				assert.Contains(t, names, "NEW_VAR")
				require.Contains(t, names, "NEW_SECRET")
				assert.Empty(t, names["NEW_SECRET"].Default, "secret default must be cleared in drift")

				require.NotNil(t, res.ConfigDrift)
				require.NotNil(t, res.ConfigDrift.Transport)
				assert.Equal(t, "stdio", res.ConfigDrift.Transport.From)
				assert.Equal(t, "streamable-http", res.ConfigDrift.Transport.To)
				require.NotNil(t, res.ConfigDrift.PermissionProfile)
				assert.Equal(t, "none", res.ConfigDrift.PermissionProfile.From)
				assert.Equal(t, "network", res.ConfigDrift.PermissionProfile.To)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			mockProvider := registrymocks.NewMockProvider(ctrl)
			tt.setupMock(mockProvider)

			checker, err := NewChecker(mockProvider)
			require.NoError(t, err)

			res, err := checker.Check(t.Context(), tt.cfg)
			require.NoError(t, err)
			require.NotNil(t, res)

			assert.Equal(t, tt.wantStatus, res.Status)
			if tt.wantReason {
				assert.NotEmpty(t, res.Reason)
			}
			if tt.assertMore != nil {
				tt.assertMore(t, res)
			}
		})
	}
}

func TestChecker_Check_NilConfig(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	checker, err := NewChecker(registrymocks.NewMockProvider(ctrl))
	require.NoError(t, err)

	res, err := checker.Check(t.Context(), nil)
	require.Error(t, err)
	assert.Nil(t, res)
}

func TestChecker_CheckAll(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	mockProvider := registrymocks.NewMockProvider(ctrl)
	mockProvider.EXPECT().GetServer(testServerName).Return(imageMeta("ghcr.io/example/server:1.2.0"), nil)

	checker, err := NewChecker(mockProvider)
	require.NoError(t, err)

	configs := []*runner.RunConfig{
		nil, // skipped
		{Name: "raw", Image: "ghcr.io/example/server:1.0.0"},                                    // not-registry-sourced
		{Name: "wl", Image: "ghcr.io/example/server:1.0.0", RegistryServerName: testServerName}, // upgrade-available
	}

	results := checker.CheckAll(t.Context(), configs)
	require.Len(t, results, 2, "nil config must be skipped")
	assert.Equal(t, StatusNotRegistrySourced, results[0].Status)
	assert.Equal(t, StatusUpgradeAvailable, results[1].Status)
}

func TestComputeEnvDrift_NoDrift(t *testing.T) {
	t.Parallel()
	cfg := &runner.RunConfig{
		EnvVars: map[string]string{"FOO": "bar"},
		Secrets: []string{"sk,target=TOKEN"},
	}
	meta := imageMeta("ghcr.io/example/server:1.0.0")
	meta.EnvVars = []*regtypes.EnvVar{
		{Name: "FOO"},
		{Name: "TOKEN", Secret: true},
		nil, // tolerated
	}
	assert.Nil(t, computeEnvDrift(cfg, meta))
}

func TestComputeConfigDrift_GracefulDegradation(t *testing.T) {
	t.Parallel()

	t.Run("nil permissions and empty transport do not drift", func(t *testing.T) {
		t.Parallel()
		cfg := &runner.RunConfig{
			Transport:                   transporttypes.TransportTypeStdio,
			PermissionProfileNameOrPath: "custom-profile",
		}
		meta := imageMeta("ghcr.io/example/server:1.0.0")
		meta.Transport = "" // registry declares no transport
		meta.Permissions = nil
		assert.Nil(t, computeConfigDrift(cfg, meta))
	})

	t.Run("network isolation alone does not drift", func(t *testing.T) {
		t.Parallel()
		// The registry has no network-isolation field, so a workload's own
		// isolation choice is not registry-driven drift and must not be
		// reported (it would otherwise fire for every isolated workload).
		cfg := &runner.RunConfig{IsolateNetwork: true}
		meta := imageMeta("ghcr.io/example/server:1.0.0")
		meta.Transport = ""
		assert.Nil(t, computeConfigDrift(cfg, meta))
	})
}

// imageMeta builds a minimal ImageMetadata for the given image reference.
func imageMeta(image string) *regtypes.ImageMetadata {
	return &regtypes.ImageMetadata{Image: image}
}

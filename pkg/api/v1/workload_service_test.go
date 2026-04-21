// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/templates"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/secrets"
	workloadsmocks "github.com/stacklok/toolhive/pkg/workloads/mocks"
)

func TestWorkloadService_GetWorkloadNamesFromRequest(t *testing.T) {
	t.Parallel()

	t.Run("with names", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{configProvider: config.NewDefaultProvider()}

		req := bulkOperationRequest{
			Names: []string{"workload1", "workload2"},
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, []string{"workload1", "workload2"}, result)
	})

	t.Run("with group", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "test-group").
			Return(true, nil)

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockWorkloadManager.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), "test-group").
			Return([]string{"workload1", "workload2"}, nil)

		service := &WorkloadService{
			groupManager:    mockGroupManager,
			workloadManager: mockWorkloadManager,
			configProvider:  config.NewDefaultProvider(),
		}

		req := bulkOperationRequest{
			Group: "test-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		require.NoError(t, err)
		assert.Equal(t, []string{"workload1", "workload2"}, result)
	})

	t.Run("invalid group name", func(t *testing.T) {
		t.Parallel()

		service := &WorkloadService{configProvider: config.NewDefaultProvider()}

		req := bulkOperationRequest{
			Group: "invalid-group-name-with-special-chars!@#",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "invalid group name")
	})

	t.Run("group does not exist", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "non-existent-group").
			Return(false, nil)

		service := &WorkloadService{
			groupManager:   mockGroupManager,
			configProvider: config.NewDefaultProvider(),
		}

		req := bulkOperationRequest{
			Group: "non-existent-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "group 'non-existent-group' does not exist")
	})

	t.Run("list workloads error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockGroupManager := groupsmocks.NewMockManager(ctrl)
		mockGroupManager.EXPECT().
			Exists(gomock.Any(), "test-group").
			Return(true, nil)

		mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)
		mockWorkloadManager.EXPECT().
			ListWorkloadsInGroup(gomock.Any(), "test-group").
			Return(nil, errors.New("database error"))

		service := &WorkloadService{
			groupManager:    mockGroupManager,
			workloadManager: mockWorkloadManager,
			configProvider:  config.NewDefaultProvider(),
		}

		req := bulkOperationRequest{
			Group: "test-group",
		}

		result, err := service.GetWorkloadNamesFromRequest(context.Background(), req)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to list workloads in group")
	})
}

func TestNewWorkloadService(t *testing.T) {
	t.Parallel()

	service := NewWorkloadService(nil, nil, nil, false)
	require.NotNil(t, service)
	assert.NotNil(t, service.configProvider,
		"configProvider must be initialized so config is read fresh on each call, not snapshotted at construction")
	assert.Equal(t, retriever.VerifyImageWarn, service.imageVerification,
		"imageVerification must default to warn so registry-resolved and imageRetriever paths stay consistent")
}

// TestBuildFullRunConfig_ThreadsImageVerification verifies the imageRetriever path
// uses s.imageVerification rather than a hardcoded value. Paired with the registry-
// resolved path's direct call to retriever.VerifyImage(imageURL, imageMetadata,
// s.imageVerification), this ensures both paths read the mode from the same field.
func TestBuildFullRunConfig_ThreadsImageVerification(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockGroupManager := groupsmocks.NewMockManager(ctrl)
	mockGroupManager.EXPECT().Exists(gomock.Any(), "default").Return(true, nil)

	const testImage = "test-image"

	var observed string
	mockRetriever := func(
		_ context.Context,
		_ string, _ string,
		verificationType string,
		_ string,
		_ *templates.RuntimeConfig,
	) (string, regtypes.ServerMetadata, error) {
		observed = verificationType
		return testImage, &regtypes.ImageMetadata{Image: testImage}, nil
	}

	service := &WorkloadService{
		groupManager:      mockGroupManager,
		imageRetriever:    mockRetriever,
		imagePuller:       func(_ context.Context, _ string) error { return nil },
		configProvider:    config.NewDefaultProvider(),
		imageVerification: retriever.VerifyImageDisabled,
	}

	req := &createRequest{
		Name:          "testserver",
		updateRequest: updateRequest{Image: testImage},
	}

	_, err := service.BuildFullRunConfig(context.Background(), req, 0)
	require.NoError(t, err)
	assert.Equal(t, retriever.VerifyImageDisabled, observed,
		"imageRetriever must receive s.imageVerification verbatim")
}

// writeFactorySentinelConfig writes a YAML config file with DisableUsageMetrics: true
// as a sentinel value and returns its path.
func writeFactorySentinelConfig(t *testing.T, dir string) string {
	t.Helper()
	configPath := dir + "/config.yaml"
	require.NoError(t, os.WriteFile(configPath, []byte("disable_usage_metrics: true\n"), 0600))
	return configPath
}

// TestNewWorkloadService_RespectsRegisteredFactory verifies that NewWorkloadService
// uses config.NewProvider() (which checks the registered ProviderFactory) rather than
// config.NewDefaultProvider() (which always uses the default XDG path and bypasses factories).
//
//nolint:paralleltest // Mutates global state: config.registeredFactory
func TestNewWorkloadService_RespectsRegisteredFactory(t *testing.T) {
	configPath := writeFactorySentinelConfig(t, t.TempDir())

	config.RegisterProviderFactory(func() config.Provider {
		return config.NewPathProvider(configPath)
	})
	t.Cleanup(func() { config.RegisterProviderFactory(nil) })

	service := NewWorkloadService(nil, nil, nil, false)
	require.NotNil(t, service)

	cfg := service.configProvider.GetConfig()
	assert.True(t, cfg.DisableUsageMetrics,
		"configProvider must use the factory-backed provider — DisableUsageMetrics is the sentinel set by the factory config")
}

func TestRuntimeConfigFromRequest(t *testing.T) {
	t.Parallel()

	t.Run("nil request", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, runtimeConfigFromRequest(nil))
	})

	t.Run("nil runtime config", func(t *testing.T) {
		t.Parallel()
		req := &createRequest{}
		assert.Nil(t, runtimeConfigFromRequest(req))
	})

	t.Run("empty runtime config returns nil", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			updateRequest: updateRequest{
				RuntimeConfig: &templates.RuntimeConfig{
					BuilderImage:       "   ",
					AdditionalPackages: []string{"", "   "},
				},
			},
		}

		assert.Nil(t, runtimeConfigFromRequest(req))
	})

	t.Run("trims builder image", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			updateRequest: updateRequest{
				RuntimeConfig: &templates.RuntimeConfig{
					BuilderImage: "  golang:1.24-alpine  ",
				},
			},
		}

		result := runtimeConfigFromRequest(req)
		require.NotNil(t, result)
		assert.Equal(t, "golang:1.24-alpine", result.BuilderImage)
	})

	t.Run("trims and filters additional packages", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			updateRequest: updateRequest{
				RuntimeConfig: &templates.RuntimeConfig{
					AdditionalPackages: []string{" git ", "", "  ", "curl"},
				},
			},
		}

		result := runtimeConfigFromRequest(req)
		require.NotNil(t, result)
		assert.Equal(t, []string{"git", "curl"}, result.AdditionalPackages)
	})

	t.Run("copies runtime config", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			updateRequest: updateRequest{
				RuntimeConfig: &templates.RuntimeConfig{
					BuilderImage:       "golang:1.24-alpine",
					AdditionalPackages: []string{"git"},
				},
			},
		}

		result := runtimeConfigFromRequest(req)
		require.NotNil(t, result)
		assert.Equal(t, "golang:1.24-alpine", result.BuilderImage)
		assert.Equal(t, []string{"git"}, result.AdditionalPackages)

		// Verify a copy is made for slice fields.
		req.RuntimeConfig.AdditionalPackages[0] = "curl"
		assert.Equal(t, []string{"git"}, result.AdditionalPackages)
	})
}

func TestRuntimeConfigForImageBuild(t *testing.T) {
	t.Parallel()

	t.Run("nil override returns nil", func(t *testing.T) {
		t.Parallel()

		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{Image: "go://github.com/example/server"}},
			nil,
		)
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("rejects non protocol image", func(t *testing.T) {
		t.Parallel()

		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{Image: "nginx:latest"}},
			&templates.RuntimeConfig{BuilderImage: "golang:1.24-alpine"},
		)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "runtime_config is only supported for protocol-scheme images")
	})

	t.Run("rejects remote url requests", func(t *testing.T) {
		t.Parallel()

		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{URL: "https://example.com"}},
			&templates.RuntimeConfig{BuilderImage: "golang:1.24-alpine"},
		)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "runtime_config is only supported for protocol-scheme images")
	})

	t.Run("rejects invalid builder image", func(t *testing.T) {
		t.Parallel()

		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{Image: "go://github.com/example/server"}},
			&templates.RuntimeConfig{BuilderImage: "not a valid image ref"},
		)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "runtime_config.builder_image must be a valid container image reference")
	})

	t.Run("rejects invalid additional package names", func(t *testing.T) {
		t.Parallel()

		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{Image: "go://github.com/example/server"}},
			&templates.RuntimeConfig{AdditionalPackages: []string{"curl;rm -rf /"}},
		)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "runtime_config.additional_packages contains invalid package name")
	})

	t.Run("rejects option like additional package names", func(t *testing.T) {
		t.Parallel()

		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{Image: "go://github.com/example/server"}},
			&templates.RuntimeConfig{AdditionalPackages: []string{"--allow-untrusted"}},
		)
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "runtime_config.additional_packages contains invalid package name")
	})

	t.Run("merges override with base defaults for protocol images", func(t *testing.T) {
		t.Parallel()

		override := &templates.RuntimeConfig{
			BuilderImage:       "golang:1.24-alpine",
			AdditionalPackages: []string{"curl"},
		}
		result, err := runtimeConfigForImageBuild(
			&createRequest{updateRequest: updateRequest{Image: "go://github.com/example/server"}},
			override,
		)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "golang:1.24-alpine", result.BuilderImage)

		base := getBaseRuntimeConfig(templates.TransportTypeGO)
		expectedPackages := append([]string{}, base.AdditionalPackages...)
		expectedPackages = append(expectedPackages, "curl")
		assert.Equal(t, expectedPackages, result.AdditionalPackages)

		override.AdditionalPackages[0] = "git"
		assert.Equal(t, expectedPackages, result.AdditionalPackages)
	})
}

// testDenyPolicyGate is a test helper that always blocks server creation with
// the configured error.
type testDenyPolicyGate struct {
	runner.NoopPolicyGate
	err error
}

func (g *testDenyPolicyGate) CheckCreateServer(_ context.Context, _ *runner.RunConfig) error {
	return g.err
}

// TestCreateWorkloadFromRequest_PolicyGateDenied verifies that
// CreateWorkloadFromRequest returns an error immediately when the policy gate
// blocks the operation, and that RunWorkloadDetached is never called.
//
//nolint:paralleltest // Mutates the global policy gate.
func TestCreateWorkloadFromRequest_PolicyGateDenied(t *testing.T) {
	sentinel := errors.New("blocked by test policy gate")

	// Save and restore the global gate around the test.
	original := runner.ActivePolicyGate()
	runner.RegisterPolicyGate(&testDenyPolicyGate{err: sentinel})
	t.Cleanup(func() { runner.RegisterPolicyGate(original) })

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// The group manager must confirm the "default" group exists so that
	// BuildFullRunConfig can reach the policy check without failing earlier.
	mockGroupManager := groupsmocks.NewMockManager(ctrl)
	mockGroupManager.EXPECT().
		Exists(gomock.Any(), "default").
		Return(true, nil)

	// No RunWorkloadDetached expectation: any unexpected call will cause gomock
	// to fail the test, verifying that the policy gate stops execution early.
	mockWorkloadManager := workloadsmocks.NewMockManager(ctrl)

	service := &WorkloadService{
		groupManager:    mockGroupManager,
		workloadManager: mockWorkloadManager,
		configProvider:  config.NewDefaultProvider(),
		// imageRetriever and imagePuller are nil because req.URL != "" means the
		// local image pull path is skipped entirely.
	}

	req := &createRequest{
		Name: "testserver",
		updateRequest: updateRequest{
			URL: "https://mcp.example.com/mcp",
		},
	}

	_, err := service.CreateWorkloadFromRequest(context.Background(), req)

	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
}

func TestApplyImageDefaults(t *testing.T) {
	t.Parallel()

	permProfile := &permissions.Profile{}

	baseMetadata := func() *regtypes.ImageMetadata {
		return &regtypes.ImageMetadata{
			Image:       "ghcr.io/stacklok/fetch:latest",
			TargetPort:  8080,
			Args:        []string{"--listen", "0.0.0.0"},
			Permissions: permProfile,
			EnvVars: []*regtypes.EnvVar{
				{Name: "LOG_LEVEL", Default: "info"},
				{Name: "REGION", Default: "us-east-1"},
				{Name: "API_KEY"}, // no default — should not be inserted
			},
		}
	}

	tests := []struct {
		name        string
		req         *createRequest
		wantImage   string
		wantTarget  int
		wantArgs    []string
		wantPermSet bool
		wantEnvVars map[string]string
	}{
		{
			name:        "empty request fills all defaults",
			req:         &createRequest{},
			wantImage:   "ghcr.io/stacklok/fetch:latest",
			wantTarget:  8080,
			wantArgs:    []string{"--listen", "0.0.0.0"},
			wantPermSet: true,
			wantEnvVars: map[string]string{
				"LOG_LEVEL": "info",
				"REGION":    "us-east-1",
			},
		},
		{
			name: "user image takes precedence over registry image",
			req: &createRequest{
				updateRequest: updateRequest{Image: "my-registry/custom:v1"},
			},
			wantImage:   "my-registry/custom:v1",
			wantTarget:  8080,
			wantArgs:    []string{"--listen", "0.0.0.0"},
			wantPermSet: true,
			wantEnvVars: map[string]string{
				"LOG_LEVEL": "info",
				"REGION":    "us-east-1",
			},
		},
		{
			name: "user target port takes precedence",
			req: &createRequest{
				updateRequest: updateRequest{TargetPort: 9090},
			},
			wantImage:   "ghcr.io/stacklok/fetch:latest",
			wantTarget:  9090,
			wantArgs:    []string{"--listen", "0.0.0.0"},
			wantPermSet: true,
			wantEnvVars: map[string]string{
				"LOG_LEVEL": "info",
				"REGION":    "us-east-1",
			},
		},
		{
			name: "user cmd arguments take precedence",
			req: &createRequest{
				updateRequest: updateRequest{CmdArguments: []string{"--debug"}},
			},
			wantImage:   "ghcr.io/stacklok/fetch:latest",
			wantTarget:  8080,
			wantArgs:    []string{"--debug"},
			wantPermSet: true,
			wantEnvVars: map[string]string{
				"LOG_LEVEL": "info",
				"REGION":    "us-east-1",
			},
		},
		{
			name: "user env var override preserved, other defaults filled",
			req: &createRequest{
				updateRequest: updateRequest{
					EnvVars: map[string]string{"LOG_LEVEL": "debug"},
				},
			},
			wantImage:   "ghcr.io/stacklok/fetch:latest",
			wantTarget:  8080,
			wantArgs:    []string{"--listen", "0.0.0.0"},
			wantPermSet: true,
			wantEnvVars: map[string]string{
				"LOG_LEVEL": "debug",
				"REGION":    "us-east-1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			applyImageDefaults(tt.req, baseMetadata())

			assert.Equal(t, tt.wantImage, tt.req.Image)
			assert.Equal(t, tt.wantTarget, tt.req.TargetPort)
			assert.Equal(t, tt.wantArgs, tt.req.CmdArguments)
			if tt.wantPermSet {
				assert.NotNil(t, tt.req.PermissionProfile)
			}
			assert.Equal(t, tt.wantEnvVars, tt.req.EnvVars)
		})
	}
}

func TestApplyImageDefaults_UserPermissionProfilePreserved(t *testing.T) {
	t.Parallel()

	userProfile := &permissions.Profile{Name: "user-provided"}
	registryProfile := &permissions.Profile{Name: "registry-default"}

	req := &createRequest{
		updateRequest: updateRequest{PermissionProfile: userProfile},
	}
	md := &regtypes.ImageMetadata{Permissions: registryProfile}

	applyImageDefaults(req, md)

	assert.Same(t, userProfile, req.PermissionProfile,
		"user-provided permission profile must not be replaced by the registry default")
}

func TestApplyRemoteDefaults(t *testing.T) {
	t.Parallel()

	baseMetadata := func() *regtypes.RemoteServerMetadata {
		return &regtypes.RemoteServerMetadata{
			URL: "https://mcp.example.com/mcp",
			Headers: []*regtypes.Header{
				{Name: "X-API-Key"},
			},
		}
	}

	tests := []struct {
		name        string
		req         *createRequest
		wantURL     string
		wantHeaders int
	}{
		{
			name:        "empty request fills URL and Headers",
			req:         &createRequest{},
			wantURL:     "https://mcp.example.com/mcp",
			wantHeaders: 1,
		},
		{
			name: "user URL takes precedence",
			req: &createRequest{
				updateRequest: updateRequest{URL: "https://override.example.com/mcp"},
			},
			wantURL:     "https://override.example.com/mcp",
			wantHeaders: 1,
		},
		{
			name: "user headers take precedence over registry headers",
			req: &createRequest{
				updateRequest: updateRequest{
					Headers: []*regtypes.Header{{Name: "Authorization"}},
				},
			},
			wantURL:     "https://mcp.example.com/mcp",
			wantHeaders: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			applyRemoteDefaults(tt.req, baseMetadata())

			assert.Equal(t, tt.wantURL, tt.req.URL)
			assert.Len(t, tt.req.Headers, tt.wantHeaders)
		})
	}
}

func TestBuildRemoteAuthConfigFromMetadata(t *testing.T) {
	t.Parallel()

	baseMetadata := func() *regtypes.RemoteServerMetadata {
		return &regtypes.RemoteServerMetadata{
			URL:       "https://mcp.example.com/mcp",
			ProxyPort: 4444,
			Headers:   []*regtypes.Header{{Name: "X-API-Key"}},
			EnvVars:   []*regtypes.EnvVar{{Name: "REGION", Default: "us-east-1"}},
			OAuthConfig: &regtypes.OAuthConfig{
				Issuer:       "https://issuer.example.com",
				AuthorizeURL: "https://issuer.example.com/authorize",
				TokenURL:     "https://issuer.example.com/token",
				Scopes:       []string{"openid", "profile"},
				UsePKCE:      true,
				CallbackPort: 1234,
				OAuthParams:  map[string]string{"prompt": "consent"},
				Resource:     "https://resource.example.com",
			},
		}
	}

	t.Run("returns nil when metadata has no OAuthConfig", func(t *testing.T) {
		t.Parallel()

		md := baseMetadata()
		md.OAuthConfig = nil

		cfg := buildRemoteAuthConfigFromMetadata(&createRequest{}, md)

		assert.Nil(t, cfg)
	})

	t.Run("populates all OAuth fields from metadata", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			updateRequest: updateRequest{
				OAuthConfig: remoteOAuthConfig{ClientID: "user-client-id"},
			},
		}

		cfg := buildRemoteAuthConfigFromMetadata(req, baseMetadata())

		require.NotNil(t, cfg)
		assert.Equal(t, "user-client-id", cfg.ClientID)
		assert.Equal(t, []string{"openid", "profile"}, cfg.Scopes)
		assert.Equal(t, 1234, cfg.CallbackPort)
		assert.Equal(t, "https://issuer.example.com", cfg.Issuer)
		assert.Equal(t, "https://issuer.example.com/authorize", cfg.AuthorizeURL)
		assert.Equal(t, "https://issuer.example.com/token", cfg.TokenURL)
		assert.True(t, cfg.UsePKCE)
		assert.Equal(t, map[string]string{"prompt": "consent"}, cfg.OAuthParams)
		assert.Len(t, cfg.Headers, 1)
		assert.Len(t, cfg.EnvVars, 1)
	})

	t.Run("resource precedence: user value wins over metadata and URL", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			updateRequest: updateRequest{
				OAuthConfig: remoteOAuthConfig{Resource: "https://user.example.com"},
			},
		}

		cfg := buildRemoteAuthConfigFromMetadata(req, baseMetadata())

		require.NotNil(t, cfg)
		assert.Equal(t, "https://user.example.com", cfg.Resource)
	})

	t.Run("resource precedence: metadata wins over URL when user unset", func(t *testing.T) {
		t.Parallel()

		cfg := buildRemoteAuthConfigFromMetadata(&createRequest{}, baseMetadata())

		require.NotNil(t, cfg)
		assert.Equal(t, "https://resource.example.com", cfg.Resource)
	})

	t.Run("resource derived from URL when both user and metadata unset", func(t *testing.T) {
		t.Parallel()

		md := baseMetadata()
		md.OAuthConfig.Resource = ""

		cfg := buildRemoteAuthConfigFromMetadata(&createRequest{}, md)

		require.NotNil(t, cfg)
		assert.NotEmpty(t, cfg.Resource, "resource should be derived from md.URL")
	})

	t.Run("user ClientSecret is applied in CLI string format", func(t *testing.T) {
		t.Parallel()

		secret := &secrets.SecretParameter{Name: "oauth-secret", Target: "CLIENT_SECRET"}
		req := &createRequest{
			updateRequest: updateRequest{
				OAuthConfig: remoteOAuthConfig{ClientSecret: secret},
			},
		}

		cfg := buildRemoteAuthConfigFromMetadata(req, baseMetadata())

		require.NotNil(t, cfg)
		assert.Equal(t, "oauth-secret,target=CLIENT_SECRET", cfg.ClientSecret)
	})

	t.Run("user BearerToken is applied in CLI string format", func(t *testing.T) {
		t.Parallel()

		token := &secrets.SecretParameter{Name: "bearer", Target: "TOKEN"}
		req := &createRequest{
			updateRequest: updateRequest{
				OAuthConfig: remoteOAuthConfig{BearerToken: token},
			},
		}

		cfg := buildRemoteAuthConfigFromMetadata(req, baseMetadata())

		require.NotNil(t, cfg)
		assert.Equal(t, "bearer,target=TOKEN", cfg.BearerToken)
	})
}

func TestApplyRegistryDefaults(t *testing.T) {
	t.Parallel()

	t.Run("fills transport and name from metadata", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{}
		md := &regtypes.ImageMetadata{
			BaseServerMetadata: regtypes.BaseServerMetadata{
				Name:      "io.github.stacklok/fetch",
				Transport: "stdio",
			},
			Image: "ghcr.io/stacklok/fetch:latest",
		}

		applyRegistryDefaults(req, md)

		assert.Equal(t, "stdio", req.Transport)
		assert.Equal(t, "io.github.stacklok/fetch", req.Name)
		assert.Equal(t, "ghcr.io/stacklok/fetch:latest", req.Image)
	})

	t.Run("user transport and name take precedence", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{
			Name: "my-workload",
			updateRequest: updateRequest{
				Transport: "streamable-http",
			},
		}
		md := &regtypes.ImageMetadata{
			BaseServerMetadata: regtypes.BaseServerMetadata{
				Name:      "io.github.stacklok/fetch",
				Transport: "stdio",
			},
		}

		applyRegistryDefaults(req, md)

		assert.Equal(t, "streamable-http", req.Transport)
		assert.Equal(t, "my-workload", req.Name)
	})

	t.Run("dispatches to remote defaults for RemoteServerMetadata", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{}
		md := &regtypes.RemoteServerMetadata{
			BaseServerMetadata: regtypes.BaseServerMetadata{
				Name:      "remote-server",
				Transport: "streamable-http",
			},
			URL: "https://remote.example.com/mcp",
		}

		applyRegistryDefaults(req, md)

		assert.Equal(t, "streamable-http", req.Transport)
		assert.Equal(t, "remote-server", req.Name)
		assert.Equal(t, "https://remote.example.com/mcp", req.URL)
	})

	t.Run("dispatches to image defaults for ImageMetadata", func(t *testing.T) {
		t.Parallel()

		req := &createRequest{}
		md := &regtypes.ImageMetadata{
			BaseServerMetadata: regtypes.BaseServerMetadata{
				Transport: "stdio",
			},
			Image:      "ghcr.io/stacklok/fetch:latest",
			TargetPort: 8080,
		}

		applyRegistryDefaults(req, md)

		assert.Equal(t, "ghcr.io/stacklok/fetch:latest", req.Image)
		assert.Equal(t, 8080, req.TargetPort)
	})
}

func TestWorkloadService_ResolveRegistryServer_UnknownRegistry(t *testing.T) {
	t.Parallel()

	service := &WorkloadService{configProvider: config.NewDefaultProvider()}

	req := &createRequest{
		Registry: "nonexistent",
		Server:   "some-server",
	}

	metadata, err := service.resolveRegistryServer(req)

	require.Error(t, err)
	assert.Nil(t, metadata)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
	assert.Contains(t, err.Error(), `unknown registry "nonexistent"`)
}

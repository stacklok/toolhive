// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/templates"
	groupsmocks "github.com/stacklok/toolhive/pkg/groups/mocks"
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
	// configProvider must be set so GetConfig() is called at point-of-use,
	// not cached once at startup (fixes stale enterprise registry URL bug).
	assert.NotNil(t, service.configProvider,
		"configProvider must be initialized so config is read fresh on each call, not snapshotted at construction")
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

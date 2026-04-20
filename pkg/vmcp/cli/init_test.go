// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	workloadmocks "github.com/stacklok/toolhive/pkg/vmcp/workloads/mocks"
)

// newDiscovererMock creates a new MockDiscoverer for use in tests.
func newDiscovererMock(t *testing.T) *workloadmocks.MockDiscoverer {
	t.Helper()
	return workloadmocks.NewMockDiscoverer(gomock.NewController(t))
}

// testWorkload is a TypedWorkload fixture used across tests.
var testWorkload = workloads.TypedWorkload{
	Name: "my-server",
	Type: workloads.WorkloadTypeMCPServer,
}

// testBackend is the vmcp.Backend returned by the mock for testWorkload.
var testBackend = &vmcp.Backend{
	Name:          "my-server",
	BaseURL:       "http://127.0.0.1:9001/mcp",
	TransportType: "streamable-http",
}

func TestInit_WritesToWriter(t *testing.T) {
	t.Parallel()

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return([]workloads.TypedWorkload{testWorkload}, nil)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), testWorkload).Return(testBackend, nil)

	var buf bytes.Buffer
	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		Writer:     &buf,
		Discoverer: disc,
	})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "groupRef: test-group")
	assert.Contains(t, output, "my-server")
}

func TestInit_WritesToFile(t *testing.T) {
	t.Parallel()

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return([]workloads.TypedWorkload{testWorkload}, nil)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), testWorkload).Return(testBackend, nil)

	path := filepath.Join(t.TempDir(), "vmcp.yaml")
	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		OutputPath: path,
		Discoverer: disc,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "groupRef: test-group")
}

func TestInit_SkipsUnsupportedTransport(t *testing.T) {
	t.Parallel()

	unsupportedWorkload := workloads.TypedWorkload{Name: "bad-transport", Type: workloads.WorkloadTypeMCPServer}

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return(
		[]workloads.TypedWorkload{unsupportedWorkload, testWorkload}, nil,
	)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), unsupportedWorkload).Return(&vmcp.Backend{
		Name:          "bad-transport",
		BaseURL:       "http://127.0.0.1:9999/mcp",
		TransportType: "http",
	}, nil)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), testWorkload).Return(testBackend, nil)

	var buf bytes.Buffer
	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		Writer:     &buf,
		Discoverer: disc,
	})
	require.NoError(t, err)

	output := buf.String()
	assert.NotContains(t, output, "bad-transport")
	assert.Contains(t, output, "my-server")
}

func TestInit_SkipsNilBackends(t *testing.T) {
	t.Parallel()

	nilWorkload := workloads.TypedWorkload{Name: "not-ready", Type: workloads.WorkloadTypeMCPServer}

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return(
		[]workloads.TypedWorkload{nilWorkload, testWorkload}, nil,
	)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), nilWorkload).Return(nil, nil)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), testWorkload).Return(testBackend, nil)

	var buf bytes.Buffer
	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		Writer:     &buf,
		Discoverer: disc,
	})
	require.NoError(t, err)

	output := buf.String()
	assert.NotContains(t, output, "not-ready")
	assert.Contains(t, output, "my-server")
}

func TestInit_EmptyGroup(t *testing.T) {
	t.Parallel()

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "empty-group").Return([]workloads.TypedWorkload{}, nil)

	var buf bytes.Buffer
	err := Init(context.Background(), InitConfig{
		GroupName:  "empty-group",
		Writer:     &buf,
		Discoverer: disc,
	})
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "groupRef: empty-group")
	assert.Contains(t, output, "backends: []")
}

func TestInit_DiscoveryError(t *testing.T) {
	t.Parallel()

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return(nil, errors.New("connection refused"))

	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		Writer:     &bytes.Buffer{},
		Discoverer: disc,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to list workloads in group")
	assert.ErrorContains(t, err, "connection refused")
}

func TestInit_RenderedYAMLIsValid(t *testing.T) {
	t.Parallel()

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return([]workloads.TypedWorkload{testWorkload}, nil)
	disc.EXPECT().GetWorkloadAsVMCPBackend(gomock.Any(), testWorkload).Return(testBackend, nil)

	path := filepath.Join(t.TempDir(), "vmcp.yaml")
	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		OutputPath: path,
		Discoverer: disc,
	})
	require.NoError(t, err)

	loader := vmcpconfig.NewYAMLLoader(path, &env.OSReader{})
	cfg, err := loader.Load()
	require.NoError(t, err)

	validator := vmcpconfig.NewValidator()
	require.NoError(t, validator.Validate(cfg))

	assert.Equal(t, "test-group", cfg.Group)
	assert.Equal(t, "test-group-vmcp", cfg.Name)
	require.Len(t, cfg.Backends, 1)
	assert.Equal(t, "my-server", cfg.Backends[0].Name)
	assert.Equal(t, "http://127.0.0.1:9001/mcp", cfg.Backends[0].URL)
	assert.Equal(t, "streamable-http", cfg.Backends[0].Transport)
}

func TestInit_OutputFilePermissions(t *testing.T) {
	t.Parallel()

	disc := newDiscovererMock(t)
	disc.EXPECT().ListWorkloadsInGroup(gomock.Any(), "test-group").Return([]workloads.TypedWorkload{}, nil)

	path := filepath.Join(t.TempDir(), "vmcp.yaml")
	err := Init(context.Background(), InitConfig{
		GroupName:  "test-group",
		OutputPath: path,
		Discoverer: disc,
	})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestInit_NilDiscoverer(t *testing.T) {
	t.Parallel()

	err := Init(context.Background(), InitConfig{
		GroupName: "test-group",
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "discoverer is required")
}

func TestInit_EmptyGroupName(t *testing.T) {
	t.Parallel()

	err := Init(context.Background(), InitConfig{
		Discoverer: newDiscovererMock(t),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid group name")
}

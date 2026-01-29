// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

// TestNew_OptimizerEnabled tests server creation with optimizer enabled
func TestNew_OptimizerEnabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	tmpDir := t.TempDir()

	hybridRatio := 70
	cfg := &Config{
		Name:       "test-server",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       0,
		SessionTTL: 5 * time.Minute,
		OptimizerConfig: &config.OptimizerConfig{
			Enabled:            true,
			PersistPath:        filepath.Join(tmpDir, "optimizer-db"),
			HybridSearchRatio:  &hybridRatio,
			EmbeddingBackend:   "ollama",
			EmbeddingURL:       "http://localhost:11434",
			EmbeddingModel:     "all-minilm",
			EmbeddingDimension: 384,
		},
	}

	rt := router.NewDefaultRouter()
	backends := []vmcp.Backend{
		{
			ID:            "backend-1",
			Name:          "Backend 1",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	srv, err := New(ctx, cfg, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)
	require.NotNil(t, srv)
	defer func() { _ = srv.Stop(context.Background()) }()

	// Verify optimizer integration was created
	// We can't directly access optimizerIntegration, but we can verify server was created successfully
}

// TestNew_OptimizerDisabled tests server creation with optimizer disabled
func TestNew_OptimizerDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	cfg := &Config{
		Name:       "test-server",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       0,
		SessionTTL: 5 * time.Minute,
		OptimizerConfig: &config.OptimizerConfig{
			Enabled: false, // Disabled
		},
	}

	rt := router.NewDefaultRouter()
	backends := []vmcp.Backend{}

	srv, err := New(ctx, cfg, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)
	require.NotNil(t, srv)
	defer func() { _ = srv.Stop(context.Background()) }()
}

// TestNew_OptimizerConfigNil tests server creation with nil optimizer config
func TestNew_OptimizerConfigNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	cfg := &Config{
		Name:            "test-server",
		Version:         "1.0.0",
		Host:            "127.0.0.1",
		Port:            0,
		SessionTTL:      5 * time.Minute,
		OptimizerConfig: nil, // Nil config
	}

	rt := router.NewDefaultRouter()
	backends := []vmcp.Backend{}

	srv, err := New(ctx, cfg, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)
	require.NotNil(t, srv)
	defer func() { _ = srv.Stop(context.Background()) }()
}

// TestNew_OptimizerIngestionError tests error handling during optimizer ingestion
func TestNew_OptimizerIngestionError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	// Return error when listing capabilities
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(nil, assert.AnError).
		AnyTimes()

	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	tmpDir := t.TempDir()

	cfg := &Config{
		Name:       "test-server",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       0,
		SessionTTL: 5 * time.Minute,
		OptimizerConfig: &config.OptimizerConfig{
			Enabled:            true,
			PersistPath:        filepath.Join(tmpDir, "optimizer-db"),
			EmbeddingBackend:   "ollama",
			EmbeddingURL:       "http://localhost:11434",
			EmbeddingModel:     "all-minilm",
			EmbeddingDimension: 384,
		},
	}

	rt := router.NewDefaultRouter()
	backends := []vmcp.Backend{
		{
			ID:            "backend-1",
			Name:          "Backend 1",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	// Should not fail even if ingestion fails
	srv, err := New(ctx, cfg, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err, "Server should be created even if optimizer ingestion fails")
	require.NotNil(t, srv)
	defer func() { _ = srv.Stop(context.Background()) }()
}

// TestNew_OptimizerHybridRatio tests hybrid ratio configuration
func TestNew_OptimizerHybridRatio(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	tmpDir := t.TempDir()

	hybridRatio := 50 // Custom ratio
	cfg := &Config{
		Name:       "test-server",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       0,
		SessionTTL: 5 * time.Minute,
		OptimizerConfig: &config.OptimizerConfig{
			Enabled:            true,
			PersistPath:        filepath.Join(tmpDir, "optimizer-db"),
			HybridSearchRatio:  &hybridRatio,
			EmbeddingBackend:   "ollama",
			EmbeddingURL:       "http://localhost:11434",
			EmbeddingModel:     "all-minilm",
			EmbeddingDimension: 384,
		},
	}

	rt := router.NewDefaultRouter()
	backends := []vmcp.Backend{}

	srv, err := New(ctx, cfg, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)
	require.NotNil(t, srv)
	defer func() { _ = srv.Stop(context.Background()) }()
}

// TestServer_Stop_OptimizerCleanup tests optimizer cleanup on server stop
func TestServer_Stop_OptimizerCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockBackendClient.EXPECT().
		ListCapabilities(gomock.Any(), gomock.Any()).
		Return(&vmcp.CapabilityList{}, nil).
		AnyTimes()

	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockDiscoveryMgr.EXPECT().
		Discover(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	tmpDir := t.TempDir()

	cfg := &Config{
		Name:       "test-server",
		Version:    "1.0.0",
		Host:       "127.0.0.1",
		Port:       0,
		SessionTTL: 5 * time.Minute,
		OptimizerConfig: &config.OptimizerConfig{
			Enabled:            true,
			PersistPath:        filepath.Join(tmpDir, "optimizer-db"),
			EmbeddingBackend:   "ollama",
			EmbeddingURL:       "http://localhost:11434",
			EmbeddingModel:     "all-minilm",
			EmbeddingDimension: 384,
		},
	}

	rt := router.NewDefaultRouter()
	backends := []vmcp.Backend{}

	srv, err := New(ctx, cfg, rt, mockBackendClient, mockDiscoveryMgr, vmcp.NewImmutableRegistry(backends), nil)
	require.NoError(t, err)
	require.NotNil(t, srv)

	// Stop should clean up optimizer
	err = srv.Stop(context.Background())
	require.NoError(t, err)
}

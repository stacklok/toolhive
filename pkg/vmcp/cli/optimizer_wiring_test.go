// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// stubEmbeddingManager is a test double for the embeddingManager interface.
type stubEmbeddingManager struct {
	startURL  string
	startErr  error
	stopErr   error
	startSeen bool
	stopSeen  bool
}

func (s *stubEmbeddingManager) Start(_ context.Context) (string, error) {
	s.startSeen = true
	return s.startURL, s.startErr
}

func (s *stubEmbeddingManager) Stop(_ context.Context) error {
	s.stopSeen = true
	return s.stopErr
}

func TestInjectOptimizerConfig_NeitherTierEnabled(t *testing.T) {
	t.Parallel()

	vmcpCfg := &vmcpconfig.Config{}
	cfg := ServeConfig{EnableOptimizer: false, EnableEmbedding: false}

	cleanup, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, nil)

	require.NoError(t, err)
	assert.Nil(t, cleanup)
	assert.Nil(t, vmcpCfg.Optimizer, "Optimizer must remain nil when neither tier is enabled")
}

func TestInjectOptimizerConfig_Tier1Only(t *testing.T) {
	t.Parallel()

	vmcpCfg := &vmcpconfig.Config{}
	cfg := ServeConfig{EnableOptimizer: true, EnableEmbedding: false}

	cleanup, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, nil)

	require.NoError(t, err)
	assert.Nil(t, cleanup, "Tier 1 does not start TEI — no cleanup needed")
	require.NotNil(t, vmcpCfg.Optimizer)
	assert.Empty(t, vmcpCfg.Optimizer.EmbeddingService, "Tier 1 must not set an embedding service URL")
}

func TestInjectOptimizerConfig_Tier1_PreservesExistingOptimizerConfig(t *testing.T) {
	t.Parallel()

	existing := &vmcpconfig.OptimizerConfig{MaxToolsToReturn: 5}
	vmcpCfg := &vmcpconfig.Config{Optimizer: existing}
	cfg := ServeConfig{EnableOptimizer: true, EnableEmbedding: false}

	_, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, nil)

	require.NoError(t, err)
	assert.Same(t, existing, vmcpCfg.Optimizer, "Existing optimizer config must not be replaced")
}

func TestInjectOptimizerConfig_Tier2_SetsEmbeddingURL(t *testing.T) {
	t.Parallel()

	stub := &stubEmbeddingManager{startURL: "http://127.0.0.1:8080"}
	vmcpCfg := &vmcpconfig.Config{}
	cfg := ServeConfig{EnableEmbedding: true}

	cleanup, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, stub)

	require.NoError(t, err)
	require.NotNil(t, cleanup, "Tier 2 must return a cleanup func")
	assert.True(t, stub.startSeen, "Start must be called for Tier 2")
	require.NotNil(t, vmcpCfg.Optimizer)
	assert.Equal(t, "http://127.0.0.1:8080", vmcpCfg.Optimizer.EmbeddingService)
}

func TestInjectOptimizerConfig_Tier2_ImpliesOptimizer(t *testing.T) {
	t.Parallel()

	stub := &stubEmbeddingManager{startURL: "http://127.0.0.1:8080"}
	vmcpCfg := &vmcpconfig.Config{}
	// EnableOptimizer is false — EnableEmbedding alone must activate the optimizer.
	cfg := ServeConfig{EnableOptimizer: false, EnableEmbedding: true}

	_, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, stub)

	require.NoError(t, err)
	assert.NotNil(t, vmcpCfg.Optimizer, "Tier 2 must activate optimizer even without --optimizer flag")
}

func TestInjectOptimizerConfig_Tier2_StartError(t *testing.T) {
	t.Parallel()

	startErr := errors.New("docker daemon unavailable")
	stub := &stubEmbeddingManager{startErr: startErr}
	vmcpCfg := &vmcpconfig.Config{}
	cfg := ServeConfig{EnableEmbedding: true}

	cleanup, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, stub)

	require.Error(t, err)
	assert.ErrorContains(t, err, "docker daemon unavailable")
	assert.Nil(t, cleanup, "No cleanup func must be returned on Start failure")
}

func TestInjectOptimizerConfig_Tier2_NilManagerReturnsError(t *testing.T) {
	t.Parallel()

	vmcpCfg := &vmcpconfig.Config{}
	cfg := ServeConfig{EnableEmbedding: true}

	cleanup, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, nil)

	require.Error(t, err)
	assert.ErrorContains(t, err, "embedding manager must not be nil")
	assert.Nil(t, cleanup)
}

func TestInjectOptimizerConfig_Tier2_CleanupCallsStop(t *testing.T) {
	t.Parallel()

	stub := &stubEmbeddingManager{startURL: "http://127.0.0.1:9999"}
	vmcpCfg := &vmcpconfig.Config{}
	cfg := ServeConfig{EnableEmbedding: true}

	cleanup, err := injectOptimizerConfig(context.Background(), cfg, vmcpCfg, stub)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	cleanup()
	assert.True(t, stub.stopSeen, "cleanup func must call Stop on the embedding manager")
}

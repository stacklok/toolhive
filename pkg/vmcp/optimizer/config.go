// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// ConfigFromVMCPConfig converts a vmcp/config.OptimizerConfig to optimizer.Config.
// This helper function bridges the gap between the shared config package and
// the optimizer package's internal configuration structure.
func ConfigFromVMCPConfig(cfg *config.OptimizerConfig) *Config {
	if cfg == nil {
		return nil
	}

	optimizerCfg := &Config{
		Enabled:           cfg.Enabled,
		PersistPath:       cfg.PersistPath,
		FTSDBPath:         cfg.FTSDBPath,
		HybridSearchRatio: 70, // Default
	}

	// Handle HybridSearchRatio (pointer in config, value in optimizer.Config)
	if cfg.HybridSearchRatio != nil {
		optimizerCfg.HybridSearchRatio = *cfg.HybridSearchRatio
	}

	// Convert embedding config
	if cfg.EmbeddingBackend != "" || cfg.EmbeddingURL != "" || cfg.EmbeddingModel != "" || cfg.EmbeddingDimension > 0 {
		optimizerCfg.EmbeddingConfig = &embeddings.Config{
			BackendType: cfg.EmbeddingBackend,
			BaseURL:     cfg.EmbeddingURL,
			Model:       cfg.EmbeddingModel,
			Dimension:   cfg.EmbeddingDimension,
		}
	}

	return optimizerCfg
}

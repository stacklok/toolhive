// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/embeddings"
)

// Config holds optimizer configuration.
type Config struct {
	// Enabled controls whether optimizer tools are available
	Enabled bool

	// PersistPath is the optional path for chromem-go database persistence (empty = in-memory)
	PersistPath string

	// FTSDBPath is the path to SQLite FTS5 database for BM25 search
	// (empty = auto-default: ":memory:" or "{PersistPath}/fts.db")
	FTSDBPath string

	// HybridSearchRatio controls semantic vs BM25 mix (0-100 percentage, default: 70)
	HybridSearchRatio int

	// EmbeddingConfig configures the embedding backend (vLLM, Ollama, OpenAI-compatible)
	EmbeddingConfig *embeddings.Config
}

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

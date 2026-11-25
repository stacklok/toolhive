// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// NewEmbeddingClient creates an EmbeddingClient from the given optimizer
// configuration, selecting the backend implementation from EmbeddingProvider.
// It returns (nil, nil) if cfg is nil or no embedding service URL is configured,
// meaning semantic search will be disabled.
func NewEmbeddingClient(cfg *types.OptimizerConfig) (types.EmbeddingClient, error) {
	if cfg == nil || cfg.EmbeddingService == "" {
		return nil, nil
	}

	switch cfg.EmbeddingProvider {
	case "", types.EmbeddingProviderTEI:
		return newTEIClient(cfg.EmbeddingService, cfg.EmbeddingServiceTimeout)
	case types.EmbeddingProviderOpenAI:
		return newOpenAIClient(cfg.EmbeddingService, cfg.EmbeddingModel, cfg.EmbeddingAPIKey,
			cfg.EmbeddingHeaders, cfg.EmbeddingServiceTimeout)
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q (supported: %q, %q)",
			cfg.EmbeddingProvider, types.EmbeddingProviderTEI, types.EmbeddingProviderOpenAI)
	}
}

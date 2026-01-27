// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/models"
	"github.com/stacklok/toolhive/pkg/logger"
)

// HybridSearchConfig configures hybrid search behavior
type HybridSearchConfig struct {
	// SemanticRatio controls the mix of semantic vs BM25 results (0-100, representing percentage)
	// Default: 70 (70% semantic, 30% BM25)
	SemanticRatio int

	// Limit is the total number of results to return
	Limit int

	// ServerID optionally filters results to a specific server
	ServerID *string
}

// DefaultHybridConfig returns sensible defaults for hybrid search
func DefaultHybridConfig() *HybridSearchConfig {
	return &HybridSearchConfig{
		SemanticRatio: 70,
		Limit:         10,
	}
}

// searchHybrid performs hybrid search combining semantic (chromem-go) and BM25 (FTS5) results
// This matches the Python mcp-optimizer's hybrid search implementation
func (ops *backendToolOps) searchHybrid(
	ctx context.Context,
	queryText string,
	config *HybridSearchConfig,
) ([]*models.BackendToolWithMetadata, error) {
	if config == nil {
		config = DefaultHybridConfig()
	}

	// Calculate limits for each search method
	// Convert percentage to ratio (0-100 -> 0.0-1.0)
	semanticRatioFloat := float64(config.SemanticRatio) / 100.0
	semanticLimit := max(1, int(float64(config.Limit)*semanticRatioFloat))
	bm25Limit := max(1, config.Limit-semanticLimit)

	logger.Debugf(
		"Hybrid search: semantic_limit=%d, bm25_limit=%d, ratio=%d%%",
		semanticLimit, bm25Limit, config.SemanticRatio,
	)

	// Execute both searches in parallel
	type searchResult struct {
		results []*models.BackendToolWithMetadata
		err     error
	}

	semanticCh := make(chan searchResult, 1)
	bm25Ch := make(chan searchResult, 1)

	// Semantic search
	go func() {
		results, err := ops.search(ctx, queryText, semanticLimit, config.ServerID)
		semanticCh <- searchResult{results, err}
	}()

	// BM25 search
	go func() {
		results, err := ops.db.fts.SearchBM25(ctx, queryText, bm25Limit, config.ServerID)
		bm25Ch <- searchResult{results, err}
	}()

	// Collect results
	var semanticResults, bm25Results []*models.BackendToolWithMetadata
	var errs []error

	// Wait for semantic results
	semanticRes := <-semanticCh
	if semanticRes.err != nil {
		logger.Warnf("Semantic search failed: %v", semanticRes.err)
		errs = append(errs, semanticRes.err)
	} else {
		semanticResults = semanticRes.results
	}

	// Wait for BM25 results
	bm25Res := <-bm25Ch
	if bm25Res.err != nil {
		logger.Warnf("BM25 search failed: %v", bm25Res.err)
		errs = append(errs, bm25Res.err)
	} else {
		bm25Results = bm25Res.results
	}

	// If both failed, return error
	if len(errs) == 2 {
		return nil, fmt.Errorf("both search methods failed: semantic=%v, bm25=%v", errs[0], errs[1])
	}

	// Combine and deduplicate results
	combined := combineAndDeduplicateResults(semanticResults, bm25Results, config.Limit)

	logger.Infof(
		"Hybrid search completed: semantic=%d, bm25=%d, combined=%d (requested=%d)",
		len(semanticResults), len(bm25Results), len(combined), config.Limit,
	)

	return combined, nil
}

// combineAndDeduplicateResults merges semantic and BM25 results, removing duplicates
// Keeps the result with the higher similarity score for duplicates
func combineAndDeduplicateResults(
	semantic, bm25 []*models.BackendToolWithMetadata,
	limit int,
) []*models.BackendToolWithMetadata {
	// Use a map to deduplicate by tool ID
	seen := make(map[string]*models.BackendToolWithMetadata)

	// Add semantic results first (they typically have higher quality)
	for _, result := range semantic {
		seen[result.ID] = result
	}

	// Add BM25 results, only if not seen or if similarity is higher
	for _, result := range bm25 {
		if existing, exists := seen[result.ID]; exists {
			// Keep the one with higher similarity
			if result.Similarity > existing.Similarity {
				seen[result.ID] = result
			}
		} else {
			seen[result.ID] = result
		}
	}

	// Convert map to slice
	combined := make([]*models.BackendToolWithMetadata, 0, len(seen))
	for _, result := range seen {
		combined = append(combined, result)
	}

	// Sort by similarity (descending) and limit
	sortedResults := sortBySimilarity(combined)
	if len(sortedResults) > limit {
		sortedResults = sortedResults[:limit]
	}

	return sortedResults
}

// sortBySimilarity sorts results by similarity score in descending order
func sortBySimilarity(results []*models.BackendToolWithMetadata) []*models.BackendToolWithMetadata {
	// Simple bubble sort (fine for small result sets)
	sorted := make([]*models.BackendToolWithMetadata, len(results))
	copy(sorted, results)

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Similarity > sorted[i].Similarity {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

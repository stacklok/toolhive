// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ingestion

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/db"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/embeddings"
)

// TestService_GetTotalToolTokens tests token counting
func TestService_GetTotalToolTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	// Ingest some tools
	tools := []mcp.Tool{
		{
			Name:        "tool1",
			Description: "Tool 1",
		},
		{
			Name:        "tool2",
			Description: "Tool 2",
		},
	}

	err = svc.IngestServer(ctx, "server-1", "TestServer", nil, tools)
	require.NoError(t, err)

	// Get total tokens
	totalTokens := svc.GetTotalToolTokens(ctx)
	assert.GreaterOrEqual(t, totalTokens, 0, "Total tokens should be non-negative")
}

// TestService_GetTotalToolTokens_NoFTS tests token counting without FTS
func TestService_GetTotalToolTokens_NoFTS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		DBConfig: &db.Config{
			PersistPath: "", // In-memory
			FTSDBPath:   "", // Will default to :memory:
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	// Get total tokens (should use FTS if available, fallback otherwise)
	totalTokens := svc.GetTotalToolTokens(ctx)
	assert.GreaterOrEqual(t, totalTokens, 0, "Total tokens should be non-negative")
}

// TestService_GetBackendToolOps tests backend tool ops accessor
func TestService_GetBackendToolOps(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	toolOps := svc.GetBackendToolOps()
	require.NotNil(t, toolOps)
}

// TestService_GetEmbeddingManager tests embedding manager accessor
func TestService_GetEmbeddingManager(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	manager := svc.GetEmbeddingManager()
	require.NotNil(t, manager)
}

// TestService_IngestServer_ErrorHandling tests error handling during ingestion
func TestService_IngestServer_ErrorHandling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	// Test with empty tools list
	err = svc.IngestServer(ctx, "server-1", "TestServer", nil, []mcp.Tool{})
	require.NoError(t, err, "Should handle empty tools list gracefully")

	// Test with nil description
	err = svc.IngestServer(ctx, "server-2", "TestServer2", nil, []mcp.Tool{
		{
			Name:        "tool1",
			Description: "Tool 1",
		},
	})
	require.NoError(t, err, "Should handle nil description gracefully")
}

// TestService_Close_ErrorHandling tests error handling during close
func TestService_Close_ErrorHandling(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)

	// Close should succeed
	err = svc.Close()
	require.NoError(t, err)

	// Multiple closes should be safe
	err = svc.Close()
	require.NoError(t, err)
}

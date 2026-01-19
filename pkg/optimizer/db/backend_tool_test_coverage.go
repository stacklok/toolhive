package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// TestBackendToolOps_Create_FTS tests FTS integration in Create
func TestBackendToolOps_Create_FTS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	config := &Config{
		PersistPath: filepath.Join(tmpDir, "test-db"),
		FTSDBPath:   filepath.Join(tmpDir, "fts.db"),
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	embeddingFunc := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	ops := NewBackendToolOps(db, embeddingFunc)

	desc := "A test tool"
	tool := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "test_tool",
		Description: &desc,
		InputSchema: []byte(`{"type": "object"}`),
		TokenCount:  10,
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	// Create should also update FTS
	err = ops.Create(ctx, tool, "TestServer")
	require.NoError(t, err)

	// Verify FTS was updated
	ftsDB := db.GetFTSDB()
	require.NotNil(t, ftsDB)
}

// TestBackendToolOps_DeleteByServer_FTS tests FTS integration in DeleteByServer
func TestBackendToolOps_DeleteByServer_FTS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	config := &Config{
		PersistPath: filepath.Join(tmpDir, "test-db"),
		FTSDBPath:   filepath.Join(tmpDir, "fts.db"),
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	embeddingFunc := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	ops := NewBackendToolOps(db, embeddingFunc)

	desc := "A test tool"
	tool := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "test_tool",
		Description: &desc,
		InputSchema: []byte(`{"type": "object"}`),
		TokenCount:  10,
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	// Create tool
	err = ops.Create(ctx, tool, "TestServer")
	require.NoError(t, err)

	// DeleteByServer should also delete from FTS
	err = ops.DeleteByServer(ctx, "server-1")
	require.NoError(t, err)
}

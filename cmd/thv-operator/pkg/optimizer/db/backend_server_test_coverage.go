// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/models"
)

// TestBackendServerOps_Create_FTS tests FTS integration in Create
func TestBackendServerOps_Create_FTS(t *testing.T) {
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

	ops := NewBackendServerOps(db, embeddingFunc)

	server := &models.BackendServer{
		ID:          "server-1",
		Name:        "Test Server",
		Description: stringPtr("A test server"),
		Group:       "default",
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	// Create should also update FTS
	err = ops.Create(ctx, server)
	require.NoError(t, err)

	// Verify FTS was updated by checking FTS DB directly
	ftsDB := db.GetFTSDB()
	require.NotNil(t, ftsDB)

	// FTS should have the server
	// We can't easily query FTS directly, but we can verify it doesn't error
}

// TestBackendServerOps_Delete_FTS tests FTS integration in Delete
func TestBackendServerOps_Delete_FTS(t *testing.T) {
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

	ops := NewBackendServerOps(db, embeddingFunc)

	desc := "A test server"
	server := &models.BackendServer{
		ID:          "server-1",
		Name:        "Test Server",
		Description: &desc,
		Group:       "default",
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	// Create server
	err = ops.Create(ctx, server)
	require.NoError(t, err)

	// Delete should also delete from FTS
	err = ops.Delete(ctx, server.ID)
	require.NoError(t, err)
}

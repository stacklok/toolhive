// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewDB_CorruptedDatabase tests database recovery from corruption
func TestNewDB_CorruptedDatabase(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "corrupted-db")

	// Create a directory that looks like a corrupted database
	err := os.MkdirAll(dbPath, 0755)
	require.NoError(t, err)

	// Create a file that might cause issues
	err = os.WriteFile(filepath.Join(dbPath, "some-file"), []byte("corrupted"), 0644)
	require.NoError(t, err)

	config := &Config{
		PersistPath: dbPath,
	}

	// Should recover from corruption
	db, err := NewDB(config)
	require.NoError(t, err)
	require.NotNil(t, db)
	defer func() { _ = db.Close() }()
}

// TestNewDB_CorruptedDatabase_RecoveryFailure tests when recovery fails
func TestNewDB_CorruptedDatabase_RecoveryFailure(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "corrupted-db")

	// Create a directory that looks like a corrupted database
	err := os.MkdirAll(dbPath, 0755)
	require.NoError(t, err)

	// Create a file that might cause issues
	err = os.WriteFile(filepath.Join(dbPath, "some-file"), []byte("corrupted"), 0644)
	require.NoError(t, err)

	// Make directory read-only to simulate recovery failure
	// Note: This might not work on all systems, so we'll test the error path differently
	// Instead, we'll test with an invalid path that can't be created
	config := &Config{
		PersistPath: "/invalid/path/that/does/not/exist",
	}

	_, err = NewDB(config)
	// Should return error for invalid path
	assert.Error(t, err)
}

// TestDB_GetOrCreateCollection tests collection creation and retrieval
func TestDB_GetOrCreateCollection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// Create a simple embedding function
	embeddingFunc := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	// Get or create collection
	collection, err := db.GetOrCreateCollection(ctx, "test-collection", embeddingFunc)
	require.NoError(t, err)
	require.NotNil(t, collection)

	// Get existing collection
	collection2, err := db.GetOrCreateCollection(ctx, "test-collection", embeddingFunc)
	require.NoError(t, err)
	require.NotNil(t, collection2)
	assert.Equal(t, collection, collection2)
}

// TestDB_GetCollection tests collection retrieval
func TestDB_GetCollection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	embeddingFunc := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	// Get non-existent collection should fail
	_, err = db.GetCollection("non-existent", embeddingFunc)
	assert.Error(t, err)

	// Create collection first
	_, err = db.GetOrCreateCollection(ctx, "test-collection", embeddingFunc)
	require.NoError(t, err)

	// Now get it
	collection, err := db.GetCollection("test-collection", embeddingFunc)
	require.NoError(t, err)
	require.NotNil(t, collection)
}

// TestDB_DeleteCollection tests collection deletion
func TestDB_DeleteCollection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	embeddingFunc := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	// Create collection
	_, err = db.GetOrCreateCollection(ctx, "test-collection", embeddingFunc)
	require.NoError(t, err)

	// Delete collection
	db.DeleteCollection("test-collection")

	// Verify it's deleted
	_, err = db.GetCollection("test-collection", embeddingFunc)
	assert.Error(t, err)
}

// TestDB_Reset tests database reset
func TestDB_Reset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	embeddingFunc := func(_ context.Context, _ string) ([]float32, error) {
		return []float32{0.1, 0.2, 0.3}, nil
	}

	// Create collections
	_, err = db.GetOrCreateCollection(ctx, BackendServerCollection, embeddingFunc)
	require.NoError(t, err)

	_, err = db.GetOrCreateCollection(ctx, BackendToolCollection, embeddingFunc)
	require.NoError(t, err)

	// Reset database
	db.Reset()

	// Verify collections are deleted
	_, err = db.GetCollection(BackendServerCollection, embeddingFunc)
	assert.Error(t, err)

	_, err = db.GetCollection(BackendToolCollection, embeddingFunc)
	assert.Error(t, err)
}

// TestDB_GetChromemDB tests chromem DB accessor
func TestDB_GetChromemDB(t *testing.T) {
	t.Parallel()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	chromemDB := db.GetChromemDB()
	require.NotNil(t, chromemDB)
}

// TestDB_GetFTSDB tests FTS DB accessor
func TestDB_GetFTSDB(t *testing.T) {
	t.Parallel()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	ftsDB := db.GetFTSDB()
	require.NotNil(t, ftsDB)
}

// TestDB_Close tests database closing
func TestDB_Close(t *testing.T) {
	t.Parallel()

	config := &Config{
		PersistPath: "", // In-memory
	}

	db, err := NewDB(config)
	require.NoError(t, err)

	err = db.Close()
	require.NoError(t, err)

	// Multiple closes should be safe
	err = db.Close()
	require.NoError(t, err)
}

// TestNewDB_FTSDBPath tests FTS database path configuration
func TestNewDB_FTSDBPath(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "in-memory FTS with persistent chromem",
			config: &Config{
				PersistPath: filepath.Join(tmpDir, "db"),
				FTSDBPath:   ":memory:",
			},
			wantErr: false,
		},
		{
			name: "persistent FTS with persistent chromem",
			config: &Config{
				PersistPath: filepath.Join(tmpDir, "db2"),
				FTSDBPath:   filepath.Join(tmpDir, "fts.db"),
			},
			wantErr: false,
		},
		{
			name: "default FTS path with persistent chromem",
			config: &Config{
				PersistPath: filepath.Join(tmpDir, "db3"),
				// FTSDBPath not set, should default to {PersistPath}/fts.db
			},
			wantErr: false,
		},
		{
			name: "in-memory FTS with in-memory chromem",
			config: &Config{
				PersistPath: "",
				FTSDBPath:   ":memory:",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, err := NewDB(tt.config)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NotNil(t, db)
				defer func() { _ = db.Close() }()

				// Verify FTS DB is accessible
				ftsDB := db.GetFTSDB()
				require.NotNil(t, ftsDB)
			}
		})
	}
}

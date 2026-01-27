// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/philippgille/chromem-go"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Config holds database configuration
//
// The optimizer database is designed to be ephemeral - it's rebuilt from scratch
// on each startup by ingesting MCP backends. Persistence is optional and primarily
// useful for development/debugging to avoid re-generating embeddings.
type Config struct {
	// PersistPath is the optional path for chromem-go persistence.
	// If empty, chromem-go will be in-memory only (recommended for production).
	PersistPath string

	// FTSDBPath is the path for SQLite FTS5 database for BM25 search.
	// If empty, defaults to ":memory:" for in-memory FTS5, or "{PersistPath}/fts.db" if PersistPath is set.
	// FTS5 is always enabled for hybrid search.
	FTSDBPath string
}

// chromemDB represents the hybrid database (chromem-go + SQLite FTS5) for optimizer data
// This is a private implementation detail. Use the Database interface instead.
type chromemDB struct {
	config  *Config
	chromem *chromem.DB  // Vector/semantic search
	fts     *FTSDatabase // BM25 full-text search (optional)
	mu      sync.RWMutex
}

// Collection names
//
// Terminology: We use "backend_servers" and "backend_tools" to be explicit about
// tracking MCP server metadata. While vMCP uses "Backend" for the workload concept,
// the optimizer focuses on the MCP server component for semantic search and tool discovery.
// This naming convention provides clarity across the database layer.
const (
	BackendServerCollection = "backend_servers"
	BackendToolCollection   = "backend_tools"
)

// newChromemDB creates a new chromem-go database with FTS5 for hybrid search
// This is a private function. Use NewDatabase instead.
func newChromemDB(config *Config) (*chromemDB, error) {
	var chromemInstance *chromem.DB
	var err error

	if config.PersistPath != "" {
		logger.Infof("Creating chromem-go database with persistence at: %s", config.PersistPath)
		chromemInstance, err = chromem.NewPersistentDB(config.PersistPath, false)
		if err != nil {
			// Check if error is due to corrupted database (missing collection metadata)
			if strings.Contains(err.Error(), "collection metadata file not found") {
				logger.Warnf("Database appears corrupted, attempting to remove and recreate: %v", err)
				// Try to remove corrupted database directory
				// Use RemoveAll which should handle directories recursively
				// If it fails, we'll try to create with a new path or fall back to in-memory
				if removeErr := os.RemoveAll(config.PersistPath); removeErr != nil {
					logger.Warnf("Failed to remove corrupted database directory (may be in use): %v. Will try to recreate anyway.", removeErr)
					// Try to rename the corrupted directory and create a new one
					backupPath := config.PersistPath + ".corrupted"
					if renameErr := os.Rename(config.PersistPath, backupPath); renameErr != nil {
						logger.Warnf("Failed to rename corrupted database: %v. Attempting to create database anyway.", renameErr)
						// Continue and let chromem-go handle it - it might work if the corruption is partial
					} else {
						logger.Infof("Renamed corrupted database to: %s", backupPath)
					}
				}
				// Retry creating the database
				chromemInstance, err = chromem.NewPersistentDB(config.PersistPath, false)
				if err != nil {
					// If still failing, return the error but suggest manual cleanup
					return nil, fmt.Errorf(
						"failed to create persistent database after cleanup attempt. Please manually remove %s and try again: %w",
						config.PersistPath, err)
				}
				logger.Info("Successfully recreated database after cleanup")
			} else {
				return nil, fmt.Errorf("failed to create persistent database: %w", err)
			}
		}
	} else {
		logger.Info("Creating in-memory chromem-go database")
		chromemInstance = chromem.NewDB()
	}

	db := &chromemDB{
		config:  config,
		chromem: chromemInstance,
	}

	// Set default FTS5 path if not provided
	ftsPath := config.FTSDBPath
	if ftsPath == "" {
		if config.PersistPath != "" {
			// Persistent mode: store FTS5 alongside chromem-go
			ftsPath = config.PersistPath + "/fts.db"
		} else {
			// In-memory mode: use SQLite in-memory database
			ftsPath = ":memory:"
		}
	}

	// Initialize FTS5 database for BM25 text search (always enabled)
	logger.Infof("Initializing FTS5 database for hybrid search at: %s", ftsPath)
	ftsDB, err := NewFTSDatabase(&FTSConfig{DBPath: ftsPath})
	if err != nil {
		return nil, fmt.Errorf("failed to create FTS5 database: %w", err)
	}
	db.fts = ftsDB
	logger.Info("Hybrid search enabled (chromem-go + FTS5)")

	logger.Info("Optimizer database initialized successfully")
	return db, nil
}

// getOrCreateCollection gets an existing collection or creates a new one
func (db *chromemDB) getOrCreateCollection(
	_ context.Context,
	name string,
	embeddingFunc chromem.EmbeddingFunc,
) (*chromem.Collection, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Try to get existing collection first
	collection := db.chromem.GetCollection(name, embeddingFunc)
	if collection != nil {
		return collection, nil
	}

	// Create new collection if it doesn't exist
	collection, err := db.chromem.CreateCollection(name, nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("failed to create collection %s: %w", name, err)
	}

	logger.Debugf("Created new collection: %s", name)
	return collection, nil
}

// getCollection gets an existing collection
func (db *chromemDB) getCollection(name string, embeddingFunc chromem.EmbeddingFunc) (*chromem.Collection, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	collection := db.chromem.GetCollection(name, embeddingFunc)
	if collection == nil {
		return nil, fmt.Errorf("collection not found: %s", name)
	}
	return collection, nil
}

// deleteCollection deletes a collection
func (db *chromemDB) deleteCollection(name string) {
	db.mu.Lock()
	defer db.mu.Unlock()

	//nolint:errcheck,gosec // DeleteCollection in chromem-go doesn't return an error
	db.chromem.DeleteCollection(name)
	logger.Debugf("Deleted collection: %s", name)
}

// close closes both databases
func (db *chromemDB) close() error {
	logger.Info("Closing optimizer databases")
	// chromem-go doesn't need explicit close, but FTS5 does
	if db.fts != nil {
		if err := db.fts.Close(); err != nil {
			return fmt.Errorf("failed to close FTS database: %w", err)
		}
	}
	return nil
}

// getChromemDB returns the underlying chromem.DB instance
func (db *chromemDB) getChromemDB() *chromem.DB {
	return db.chromem
}

// getFTSDB returns the FTS database (may be nil if FTS is disabled)
func (db *chromemDB) getFTSDB() *FTSDatabase {
	return db.fts
}

// reset clears all collections and FTS tables (useful for testing and startup)
func (db *chromemDB) reset() {
	db.mu.Lock()
	defer db.mu.Unlock()

	//nolint:errcheck,gosec // DeleteCollection in chromem-go doesn't return an error
	db.chromem.DeleteCollection(BackendServerCollection)
	//nolint:errcheck,gosec // DeleteCollection in chromem-go doesn't return an error
	db.chromem.DeleteCollection(BackendToolCollection)

	// Clear FTS5 tables if available
	if db.fts != nil {
		//nolint:errcheck // Best effort cleanup
		_, _ = db.fts.db.Exec("DELETE FROM backend_tools_fts")
		//nolint:errcheck // Best effort cleanup
		_, _ = db.fts.db.Exec("DELETE FROM backend_servers_fts")
	}

	logger.Debug("Reset all collections and FTS tables")
}

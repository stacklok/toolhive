package db

import (
	"context"
	"fmt"
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

// DB represents the hybrid database (chromem-go + SQLite FTS5) for optimizer data
type DB struct {
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

// NewDB creates a new chromem-go database with FTS5 for hybrid search
func NewDB(config *Config) (*DB, error) {
	var chromemDB *chromem.DB
	var err error

	if config.PersistPath != "" {
		logger.Infof("Creating chromem-go database with persistence at: %s", config.PersistPath)
		chromemDB, err = chromem.NewPersistentDB(config.PersistPath, false)
		if err != nil {
			return nil, fmt.Errorf("failed to create persistent database: %w", err)
		}
	} else {
		logger.Info("Creating in-memory chromem-go database")
		chromemDB = chromem.NewDB()
	}

	db := &DB{
		config:  config,
		chromem: chromemDB,
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

// GetOrCreateCollection gets an existing collection or creates a new one
func (db *DB) GetOrCreateCollection(
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

// GetCollection gets an existing collection
func (db *DB) GetCollection(name string, embeddingFunc chromem.EmbeddingFunc) (*chromem.Collection, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	collection := db.chromem.GetCollection(name, embeddingFunc)
	if collection == nil {
		return nil, fmt.Errorf("collection not found: %s", name)
	}
	return collection, nil
}

// DeleteCollection deletes a collection
func (db *DB) DeleteCollection(name string) {
	db.mu.Lock()
	defer db.mu.Unlock()

	//nolint:errcheck,gosec // DeleteCollection in chromem-go doesn't return an error
	db.chromem.DeleteCollection(name)
	logger.Debugf("Deleted collection: %s", name)
}

// Close closes both databases
func (db *DB) Close() error {
	logger.Info("Closing optimizer databases")
	// chromem-go doesn't need explicit close, but FTS5 does
	if db.fts != nil {
		if err := db.fts.Close(); err != nil {
			return fmt.Errorf("failed to close FTS database: %w", err)
		}
	}
	return nil
}

// GetChromemDB returns the underlying chromem.DB instance
func (db *DB) GetChromemDB() *chromem.DB {
	return db.chromem
}

// GetFTSDB returns the FTS database (may be nil if FTS is disabled)
func (db *DB) GetFTSDB() *FTSDatabase {
	return db.fts
}

// Reset clears all collections and FTS tables (useful for testing and startup)
func (db *DB) Reset() {
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

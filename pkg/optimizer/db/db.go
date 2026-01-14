package db

import (
	"context"
	"fmt"
	"sync"

	"github.com/philippgille/chromem-go"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Config holds database configuration
type Config struct {
	// PersistPath is the optional path for persistence.
	// If empty, the database will be in-memory only.
	PersistPath string
}

// DB represents the chromem-go database with collections for optimizer data
type DB struct {
	config *Config
	db     *chromem.DB
	mu     sync.RWMutex
}

// Collection names
const (
	BackendServerCollection = "backend_servers"
	BackendToolCollection   = "backend_tools"
)

// NewDB creates a new chromem-go database
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
		config: config,
		db:     chromemDB,
	}

	logger.Info("chromem-go database initialized successfully")
	return db, nil
}

// GetOrCreateCollection gets an existing collection or creates a new one
func (db *DB) GetOrCreateCollection(ctx context.Context, name string, embeddingFunc chromem.EmbeddingFunc) (*chromem.Collection, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Try to get existing collection first
	collection := db.db.GetCollection(name, embeddingFunc)
	if collection != nil {
		return collection, nil
	}

	// Create new collection if it doesn't exist
	collection, err := db.db.CreateCollection(name, nil, embeddingFunc)
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

	collection := db.db.GetCollection(name, embeddingFunc)
	if collection == nil {
		return nil, fmt.Errorf("collection not found: %s", name)
	}
	return collection, nil
}

// DeleteCollection deletes a collection
func (db *DB) DeleteCollection(name string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.db.DeleteCollection(name)
	logger.Debugf("Deleted collection: %s", name)
	return nil
}

// Close closes the database (no-op for chromem-go, included for interface compatibility)
func (db *DB) Close() error {
	logger.Info("Closing chromem-go database")
	return nil
}

// GetDB returns the underlying chromem.DB instance
func (db *DB) GetDB() *chromem.DB {
	return db.db
}

// Reset clears all collections (useful for testing)
func (db *DB) Reset() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	db.db.DeleteCollection(BackendServerCollection)
	db.db.DeleteCollection(BackendToolCollection)
	logger.Debug("Reset all collections")
	return nil
}

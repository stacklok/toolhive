// Package db provides database operations for the optimizer.
// It manages SQLite connections and CRUD operations for MCP servers and tools.
// The database is ephemeral and recreated on each server start.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/toolhive/pkg/logger"
)

//go:embed schema.sql
var schemaSQL string

// Config holds database configuration
type Config struct {
	DBPath string
}

// DB wraps a SQL database connection with helper methods
type DB struct {
	*sql.DB
	config *Config
}

// NewDB creates a new database connection
func NewDB(config *Config) (*DB, error) {
	// Create parent directory if it doesn't exist
	dbDir := filepath.Dir(config.DBPath)
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database with extended query parameters for performance and safety
	// modernc.org/sqlite registers as "sqlite" (not "sqlite3")
	dbURL := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", config.DBPath)
	sqlDB, err := sql.Open("sqlite", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings
	sqlDB.SetMaxOpenConns(1) // SQLite supports only one writer
	sqlDB.SetMaxIdleConns(1)

	db := &DB{
		DB:     sqlDB,
		config: config,
	}

	// Load sqlite-vec extension (REQUIRED for semantic search)
	if err := db.loadExtensions(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to load sqlite-vec extension: %w\n\nThe optimizer requires sqlite-vec for semantic tool search.\nSee pkg/optimizer/README.md for setup instructions", err)
	}

	// Initialize schema (ephemeral database - no migrations needed)
	if err := db.initializeSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return db, nil
}

// loadExtensions loads SQLite extensions (sqlite-vec)
func (db *DB) loadExtensions() error {
	// Get the sqlite-vec extension path
	// In production, this should be bundled with the binary or available in the system
	// For now, we'll check if it exists and load it
	vecPath := os.Getenv("SQLITE_VEC_PATH")
	if vecPath == "" {
		// Try common locations
		possiblePaths := []string{
			"/usr/local/lib/vec0.dylib",
			"/usr/local/lib/vec0.so",
			"/usr/lib/vec0.dylib",
			"/usr/lib/vec0.so",
			"./vec0.dylib",
			"./vec0.so",
		}
		for _, path := range possiblePaths {
			if _, err := os.Stat(path); err == nil {
				vecPath = path
				break
			}
		}
	}

	if vecPath == "" {
		return fmt.Errorf("sqlite-vec extension not found. Set SQLITE_VEC_PATH environment variable")
	}

	// Use the raw connection to enable extension loading
	// This is required for go-sqlite3
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	err = conn.Raw(func(driverConn interface{}) error {
		type sqliteConn interface {
			LoadExtension(file string, entryPoint string) error
		}

		c, ok := driverConn.(sqliteConn)
		if !ok {
			return fmt.Errorf("connection does not support LoadExtension")
		}

		// Load the extension with the sqlite-vec entry point
		return c.LoadExtension(vecPath, "sqlite3_vec_init")
	})

	if err != nil {
		return fmt.Errorf("failed to load sqlite-vec extension: %w", err)
	}

	logger.Debugf("Loaded sqlite-vec extension from %s", vecPath)
	return nil
}

// initializeSchema creates the database schema
// Since this is ephemeral storage, we don't need migrations - just create everything on startup
func (db *DB) initializeSchema() error {
	// Execute the schema SQL
	// All CREATE TABLE statements use IF NOT EXISTS, so this is idempotent
	_, err := db.Exec(schemaSQL)
	if err != nil {
		return fmt.Errorf("failed to initialize schema: %w\n\nIf you see 'no such module: vec0', ensure sqlite-vec extension is loaded.\nSee pkg/optimizer/README.md for setup instructions", err)
	}

	logger.Info("Database schema initialized with vector search support")
	return nil
}


// BeginTx starts a new transaction
func (db *DB) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return db.DB.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
	})
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.DB.Close()
}

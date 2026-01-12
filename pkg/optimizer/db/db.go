package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3" // SQLite driver
	"github.com/stacklok/toolhive/pkg/logger"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

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
	dbURL := fmt.Sprintf("file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000", config.DBPath)
	sqlDB, err := sql.Open("sqlite3", dbURL)
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

	// Load sqlite-vec extension
	if err := db.loadExtensions(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to load extensions: %w", err)
	}

	// Run migrations
	if err := db.runMigrations(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
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
	conn, err := db.DB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}
	defer conn.Close()

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

// runMigrations applies database migrations
func (db *DB) runMigrations() error {
	// Create migrations table if it doesn't exist
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Read all migration files
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Apply each migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		// Check if migration was already applied
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM migrations WHERE name = ?", name).Scan(&count)
		if err != nil {
			return fmt.Errorf("failed to check migration %s: %w", name, err)
		}

		if count > 0 {
			logger.Debugf("Migration %s already applied, skipping", name)
			continue
		}

		// Read migration file
		content, err := migrationsFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", name, err)
		}

		// Execute migration
		_, err = db.Exec(string(content))
		if err != nil {
			return fmt.Errorf("failed to execute migration %s: %w", name, err)
		}

		// Record migration
		_, err = db.Exec("INSERT INTO migrations (name) VALUES (?)", name)
		if err != nil {
			return fmt.Errorf("failed to record migration %s: %w", name, err)
		}

		logger.Infof("Applied migration: %s", name)
	}

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



// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sqlite provides SQLite-backed persistent storage for ToolHive.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
	_ "modernc.org/sqlite" // SQLite driver
)

// DB wraps a *sql.DB connection to a SQLite database.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at the given path. It ensures the
// parent directory exists, configures recommended PRAGMAs for WAL mode, runs
// any pending migrations, and verifies the connection before returning.
func Open(ctx context.Context, dbPath string) (_ *DB, err error) {
	// Ensure the parent directory exists.
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("creating database directory %s: %w", dir, err)
	}

	dsn := fmt.Sprintf("file:%s?_txlock=immediate", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// If setup fails after opening, close the connection before returning.
	// The named return 'err' ensures that errors.Join propagates both the
	// original setup error and any close error to the caller.
	success := false
	defer func() {
		if !success {
			if closeErr := sqlDB.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("closing database after setup failure: %w", closeErr))
			}
		}
	}()

	// SQLite only supports a single writer, so limit to one open connection.
	sqlDB.SetMaxOpenConns(1)

	if err = applyPragmas(sqlDB); err != nil {
		return nil, fmt.Errorf("applying pragmas: %w", err)
	}

	if err = runMigrations(ctx, sqlDB); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	if err = sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("verifying database connection: %w", err)
	}

	success = true
	return &DB{db: sqlDB}, nil
}

// DefaultDBPath returns the default file path for the ToolHive SQLite database,
// located under the XDG state directory.
func DefaultDBPath() string {
	return filepath.Join(xdg.StateHome, "toolhive", "toolhive.db")
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.db.Close()
}

// DB returns the underlying *sql.DB for use by store implementations.
func (d *DB) DB() *sql.DB {
	return d.db
}

// applyPragmas configures SQLite PRAGMAs for optimal performance and safety.
func applyPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA cache_size=-2000",
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("executing %q: %w", pragma, err)
		}
	}

	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sqlite provides SQLite-backed implementations of the memory.Store
// and memory.VectorStore interfaces.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // SQLite driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB wraps a *sql.DB connection for the memory SQLite database.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the memory SQLite database at path.
func Open(ctx context.Context, path string) (_ *DB, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_txlock=immediate", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	success := false
	defer func() {
		if !success {
			if closeErr := sqlDB.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("closing database after failure: %w", closeErr))
			}
		}
	}()

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err = applyPragmas(sqlDB); err != nil {
		return nil, err
	}

	if err = runMigrations(ctx, sqlDB); err != nil {
		return nil, err
	}

	if err = sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("verifying connection: %w", err)
	}

	success = true
	return &DB{db: sqlDB}, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error { return d.db.Close() }

// DB returns the underlying *sql.DB.
func (d *DB) DB() *sql.DB { return d.db }

func applyPragmas(db *sql.DB) error {
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA cache_size=-2000",
	} {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("applying pragma %q: %w", p, err)
		}
	}
	return nil
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	migrationsFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("creating migrations sub-filesystem: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationsFS,
		goose.WithAllowOutofOrder(false),
	)
	if err != nil {
		return fmt.Errorf("creating goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}

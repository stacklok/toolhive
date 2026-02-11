// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

// runMigrations applies all pending database migrations using goose.
func runMigrations(ctx context.Context, db *sql.DB) error {
	// The embedded filesystem has files under "migrations/", so we need
	// to strip that prefix to get a flat filesystem of .sql files.
	migrationFS, err := fs.Sub(embedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	provider, err := goose.NewProvider(database.DialectSQLite3, db, migrationFS)
	if err != nil {
		return fmt.Errorf("failed to create goose provider: %w", err)
	}

	_, err = provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}

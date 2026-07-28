// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationsApply(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Verify all expected tables exist.
	tables := []string{
		"entries", "installed_skills", "skill_dependencies", "oci_tags",
		"installed_plugins", "plugin_dependencies",
	}
	for _, table := range tables {
		var name string
		err := db.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		assert.NoError(t, err, "table %q should exist", table)
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// First open applies migrations.
	db1, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	require.NoError(t, db1.Close())

	// Second open should succeed without errors (migrations already applied).
	db2, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db2.Close()

	// Verify tables still exist after re-opening.
	var count int
	err = db2.DB().QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN (" +
			"'entries', 'installed_skills', 'skill_dependencies', 'oci_tags', " +
			"'installed_plugins', 'plugin_dependencies')",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 6, count)
}

func TestMigrationsSchemaConstraints(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Insert a valid entry first.
	_, err = db.DB().Exec(`INSERT INTO entries (entry_type, name) VALUES ('skill', 'test-skill')`)
	require.NoError(t, err)

	// Verify CHECK constraint on installed_skills.scope rejects invalid values.
	_, err = db.DB().Exec(`INSERT INTO installed_skills (entry_id, scope) VALUES (1, 'invalid')`)
	assert.Error(t, err, "CHECK constraint should reject invalid scope")

	// Verify CHECK constraint on installed_skills.status rejects invalid values.
	_, err = db.DB().Exec(`INSERT INTO installed_skills (entry_id, scope, status) VALUES (1, 'user', 'bogus')`)
	assert.Error(t, err, "CHECK constraint should reject invalid status")

	// Verify valid values are accepted.
	_, err = db.DB().Exec(`INSERT INTO installed_skills (entry_id, scope, status) VALUES (1, 'user', 'installed')`)
	assert.NoError(t, err, "valid scope and status should be accepted")

	// Insert a plugin entry sharing the same name as a skill — proves the
	// entries UNIQUE(entry_type, name) constraint permits cross-type reuse.
	_, err = db.DB().Exec(`INSERT INTO entries (entry_type, name) VALUES ('plugin', 'test-skill')`)
	assert.NoError(t, err, "a plugin entry may share a name with a skill entry (different entry_type)")

	// Verify CHECK constraint on installed_plugins.scope rejects invalid values.
	_, err = db.DB().Exec(`INSERT INTO installed_plugins (entry_id, scope) VALUES (1, 'invalid')`)
	assert.Error(t, err, "CHECK constraint should reject invalid plugin scope")

	// Verify CHECK constraint on installed_plugins.status rejects invalid values.
	_, err = db.DB().Exec(`INSERT INTO installed_plugins (entry_id, scope, status) VALUES (1, 'user', 'bogus')`)
	assert.Error(t, err, "CHECK constraint should reject invalid plugin status")

	// Verify valid plugin values are accepted.
	_, err = db.DB().Exec(`INSERT INTO installed_plugins (entry_id, scope, status) VALUES (1, 'user', 'installed')`)
	assert.NoError(t, err, "valid plugin scope and status should be accepted")
}

// tableExists reports whether a table with the given name exists in the
// database's sqlite_master.
func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var name string
	err := db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
	).Scan(&name)
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, sql.ErrNoRows, "unexpected error checking table %q", table)
	return false
}

// newGooseProvider builds a goose provider over the embedded migrations,
// mirroring the construction in migrations.go so tests can drive Up/DownTo.
func newGooseProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	migrationFS, err := fs.Sub(embedMigrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(database.DialectSQLite3, db, migrationFS)
	require.NoError(t, err)
	return provider
}

// TestMigrations_ManagedFlagAppliesOverPriorState verifies migration 003 adds
// installed_skills.managed with a default of 0 (false) so rows created by an
// earlier version of the schema (001/002) are not implicitly "managed" once
// the column appears.
func TestMigrations_ManagedFlagAppliesOverPriorState(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(ctx, dbPath)
	require.NoError(t, err)
	defer db.Close()

	provider := newGooseProvider(t, db.DB())

	// Roll back to just after migration 002, before "managed" existed.
	_, err = provider.DownTo(ctx, 2)
	require.NoError(t, err)

	_, err = db.DB().Exec(`INSERT INTO entries (entry_type, name) VALUES ('skill', 'pre-migration-skill')`)
	require.NoError(t, err)
	_, err = db.DB().Exec(`INSERT INTO installed_skills (entry_id, scope) VALUES (1, 'user')`)
	require.NoError(t, err)

	// Re-apply Up: migration 003 adds the column to the pre-existing row.
	_, err = provider.Up(ctx)
	require.NoError(t, err)

	var managed int
	err = db.DB().QueryRow(`SELECT managed FROM installed_skills WHERE entry_id = 1`).Scan(&managed)
	require.NoError(t, err)
	assert.Equal(t, 0, managed, "a row created before migration 003 must default to unmanaged")

	// Down for 003 removes the column without disturbing 001/002 tables.
	_, err = provider.DownTo(ctx, 2)
	require.NoError(t, err)
	var count int
	err = db.DB().QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('installed_skills') WHERE name = 'managed'`,
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "managed column should be dropped by 003 Down")
	assert.True(t, tableExists(t, db.DB(), "installed_skills"), "installed_skills table itself must remain")
}

// TestMigrations_DownDropsPluginTables verifies that goose's Down for migration
// 002 drops the plugin tables (installed_plugins, plugin_dependencies) while
// leaving migration 001's tables (entries, installed_skills, skill_dependencies,
// oci_tags) intact. This satisfies the #5526 "migration up/down" exit gate.
func TestMigrations_DownDropsPluginTables(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Open runs all migrations up (001 + 002).
	db, err := Open(ctx, dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Both plugin tables exist after Up.
	require.True(t, tableExists(t, db.DB(), "installed_plugins"), "installed_plugins should exist after Up")
	require.True(t, tableExists(t, db.DB(), "plugin_dependencies"), "plugin_dependencies should exist after Up")

	// Roll back ONLY migration 002 (DownTo version 1), leaving 001 applied.
	provider := newGooseProvider(t, db.DB())
	_, err = provider.DownTo(ctx, 1)
	require.NoError(t, err)

	// 002's tables are gone.
	assert.False(t, tableExists(t, db.DB(), "installed_plugins"), "installed_plugins should be dropped by 002 Down")
	assert.False(t, tableExists(t, db.DB(), "plugin_dependencies"), "plugin_dependencies should be dropped by 002 Down")

	// 001's tables remain, proving 002's Down does not drop 001's tables.
	for _, table := range []string{"entries", "installed_skills", "skill_dependencies", "oci_tags"} {
		assert.True(t, tableExists(t, db.DB(), table), "table %q should remain after 002 Down", table)
	}

	// Re-applying Up re-creates the plugin tables (idempotent round-trip).
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	assert.True(t, tableExists(t, db.DB(), "installed_plugins"), "installed_plugins should exist after re-Up")
	assert.True(t, tableExists(t, db.DB(), "plugin_dependencies"), "plugin_dependencies should exist after re-Up")
}

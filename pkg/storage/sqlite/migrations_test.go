// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"path/filepath"
	"testing"

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
	tables := []string{"entries", "installed_skills", "skill_dependencies", "oci_tags"}
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
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('entries', 'installed_skills', 'skill_dependencies', 'oci_tags')",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 4, count)
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
}

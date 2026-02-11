// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpen(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	assert.NotNil(t, db.DB())
}

func TestOpenCreatesDirectory(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "nested", "dir", "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()
}

func TestClose(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)

	require.NoError(t, db.Close())

	// Verify the connection is closed by attempting a ping.
	assert.Error(t, db.DB().Ping())
}

func TestPragmas(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	tests := []struct {
		pragma   string
		expected string
	}{
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA synchronous", "1"}, // NORMAL = 1
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA cache_size", "-2000"},
	}

	for _, tt := range tests {
		var value string
		err := db.DB().QueryRow(tt.pragma).Scan(&value)
		require.NoError(t, err, "QueryRow(%q)", tt.pragma)
		assert.Equal(t, tt.expected, value, tt.pragma)
	}
}

func TestDefaultDBPath(t *testing.T) {
	t.Parallel()
	path := DefaultDBPath()
	assert.NotEmpty(t, path)
	assert.Equal(t, "toolhive.db", filepath.Base(path))
}

func TestMaxOpenConns(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	assert.Equal(t, 1, db.DB().Stats().MaxOpenConnections)
}

func TestOpenReturnsUnderlyingDB(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db, err := Open(t.Context(), dbPath)
	require.NoError(t, err)
	defer db.Close()

	assert.NotNil(t, db.DB())
}

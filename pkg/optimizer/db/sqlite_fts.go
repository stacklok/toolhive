// Package db provides database operations for the optimizer.
// This file handles FTS5 (Full-Text Search) using modernc.org/sqlite (pure Go).
package db

import (
	// Pure Go SQLite driver with FTS5 support
	_ "modernc.org/sqlite"
)

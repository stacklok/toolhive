package db

// CGO SQLite driver with FTS5 and extension loading support.
// This blank import is required in non-main packages to register the SQL driver
// for loading the sqlite-vec extension and enabling full-text search capabilities.
import _ "github.com/mattn/go-sqlite3"

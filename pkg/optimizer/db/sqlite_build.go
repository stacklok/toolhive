// +build cgo

package db

// This file forces the sqlite3 driver to be compiled with the necessary extensions
// Build tags are required for FTS5 and extension loading support

/*
#cgo CFLAGS: -DSQLITE_ENABLE_FTS5
#cgo CFLAGS: -DSQLITE_ENABLE_LOAD_EXTENSION
*/
import "C"

import (
	// Import with specific build requirements
	_ "github.com/mattn/go-sqlite3"
)


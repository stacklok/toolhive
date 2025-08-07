// Package state provides functionality for storing and retrieving runner state
// across different environments (local filesystem, Kubernetes, etc.)
package state

import (
	"context"
	"io"
)

//go:generate mockgen -destination=mocks/mock_store.go -package=mocks -source=interface.go Store

// Store defines the interface for runner state storage operations
type Store interface {
	// GetReader returns a reader for the state data
	// This is useful for streaming large state data
	GetReader(ctx context.Context, name string) (io.ReadCloser, error)

	// GetWriter returns a writer for the state data
	// This is useful for streaming large state data
	GetWriter(ctx context.Context, name string) (io.WriteCloser, error)

	// Delete removes the data for the given name
	Delete(ctx context.Context, name string) error

	// List returns all available state names
	List(ctx context.Context) ([]string, error)

	// Exists checks if data exists for the given name
	Exists(ctx context.Context, name string) (bool, error)
}

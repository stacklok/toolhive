// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package state provides functionality for storing and retrieving runner state
// across different environments (local filesystem, Kubernetes, etc.)
package state

import (
	"context"
	"fmt"
	"io"
)

// Aborter is implemented by state writers that can discard uncommitted data.
// Abort must release the writer's resources without publishing its contents.
type Aborter interface {
	Abort() error
}

// AbortWriter discards uncommitted data written to writer.
//
// Writers returned by Store.GetWriter and Store.CreateExclusive must implement
// Aborter. Callers must use AbortWriter, rather than Close, when abandoning a
// write because Close may publish the data.
func AbortWriter(writer io.WriteCloser) error {
	aborter, ok := writer.(Aborter)
	if !ok {
		return fmt.Errorf("state writer does not support abort")
	}
	return aborter.Abort()
}

//go:generate mockgen -destination=mocks/mock_store.go -package=mocks -source=interface.go Store

// Store defines the interface for runner state storage operations. Writers returned by
// GetWriter and CreateExclusive must support Aborter; callers must call AbortWriter
// rather than Close when abandoning a write, since Close may publish it.
type Store interface {
	// GetReader returns a reader for the state data
	// This is useful for streaming large state data
	GetReader(ctx context.Context, name string) (io.ReadCloser, error)

	// GetWriter returns a writer for the state data. For stores that publish state on Close,
	// callers must return Close errors because the write may not be visible until Close succeeds.
	// Callers must use AbortWriter instead of Close when abandoning a write, because Close may publish it.
	// This is useful for streaming large state data.
	GetWriter(ctx context.Context, name string) (io.WriteCloser, error)

	// CreateExclusive creates a new state entry exclusively, returning an error if it already exists.
	// For stores that publish state on Close, exclusivity is enforced at Close and Close can return
	// an error with http.StatusConflict if another writer published the entry first.
	// Callers must return Close errors and use AbortWriter rather than Close when abandoning a write,
	// because the write may not be visible until Close succeeds.
	// This provides atomic check-and-create semantics to prevent race conditions.
	CreateExclusive(ctx context.Context, name string) (io.WriteCloser, error)

	// Delete removes the data for the given name
	Delete(ctx context.Context, name string) error

	// List returns all available state names
	List(ctx context.Context) ([]string, error)

	// Exists checks if data exists for the given name
	Exists(ctx context.Context, name string) (bool, error)
}

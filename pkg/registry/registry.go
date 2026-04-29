// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

//go:generate mockgen -destination=mocks/mock_registry.go -package=mocks -source=registry.go Registry

// Registry is a queryable source of entries. Implementations may serve from
// a local file, a remote URL, or a registry-server API.
//
// Implementations must be safe for concurrent use by multiple goroutines.
//
// Get returns ErrEntryNotFound when no entry matches the (kind, name) pair.
// Search and List with an empty query return every entry the registry has,
// after applying Filter.
type Registry interface {
	// Name returns the registry's identifier. Used for the --registry NAME
	// flag and in error messages. Stable across the registry's lifetime.
	Name() string

	// Get returns the entry with the given kind and name, or
	// ErrEntryNotFound if no entry matches. Both arguments are required —
	// servers and skills can share a fully-qualified name (e.g. an MCP
	// server and an associated skill in the same publisher namespace), so
	// the kind is part of the lookup key.
	Get(kind Kind, name string) (*Entry, error)

	// List returns every entry, optionally filtered.
	List(filter Filter) ([]*Entry, error)

	// Search returns entries matching query (substring, case-insensitive
	// against name, title, and description). An empty query is treated as
	// "match all". Filter narrows the candidate set before matching.
	Search(query string, filter Filter) ([]*Entry, error)
}

// Filter narrows List and Search results. The zero value matches everything.
type Filter struct {
	// Kind, if non-empty, restricts results to entries of this kind.
	Kind Kind
}

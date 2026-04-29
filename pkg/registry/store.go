// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"
	"strings"
)

// entryKey uniquely identifies an entry within a Store. Kind is part of
// the key so a server and a skill that share a fully-qualified name (e.g.
// "io.github.user/foo") do not collide.
type entryKey struct {
	Kind Kind
	Name string
}

// Store is an in-memory Registry backed by a slice of entries indexed by
// (kind, name). It is the building block for registries that load their
// data once and serve queries from memory (file and URL sources).
//
// Registries that proxy every request to an upstream service should not use
// Store.
//
// Store is read-only after construction and safe for concurrent use.
type Store struct {
	name    string
	byKey   map[entryKey]*Entry
	ordered []*Entry
}

// NewStore returns a Store populated with the given entries.
//
// name is reported by Name(); it must be non-empty. Every entry is validated
// (see Entry.Validate); duplicates within the same kind cause an error
// rather than silent overwrite. Entries that share a name across kinds are
// allowed and disambiguated by Get(kind, name).
func NewStore(name string, entries []*Entry) (*Store, error) {
	if name == "" {
		return nil, errors.New("registry name must not be empty")
	}
	byKey := make(map[entryKey]*Entry, len(entries))
	ordered := make([]*Entry, 0, len(entries))
	for i, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("entries[%d]: %w", i, err)
		}
		key := entryKey{Kind: e.Kind, Name: e.Name}
		if _, ok := byKey[key]; ok {
			return nil, fmt.Errorf("duplicate %s entry %q in registry %q", e.Kind, e.Name, name)
		}
		byKey[key] = e
		ordered = append(ordered, e)
	}
	return &Store{
		name:    name,
		byKey:   byKey,
		ordered: ordered,
	}, nil
}

// Name returns the registry name supplied to NewStore.
func (s *Store) Name() string { return s.name }

// Get returns the entry with the given kind and name, or ErrEntryNotFound.
func (s *Store) Get(kind Kind, name string) (*Entry, error) {
	if e, ok := s.byKey[entryKey{Kind: kind, Name: name}]; ok {
		return e, nil
	}
	return nil, ErrEntryNotFound
}

// List returns every entry that matches filter, in insertion order.
func (s *Store) List(filter Filter) ([]*Entry, error) {
	out := make([]*Entry, 0, len(s.ordered))
	for _, e := range s.ordered {
		if !entryMatchesKind(e, filter.Kind) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// Search returns entries whose name, title, or description contains query
// (case-insensitive). An empty query matches every entry that passes filter.
// Order matches insertion order.
func (s *Store) Search(query string, filter Filter) ([]*Entry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]*Entry, 0)
	for _, e := range s.ordered {
		if !entryMatchesKind(e, filter.Kind) {
			continue
		}
		if q == "" || entryMatchesQuery(e, q) {
			out = append(out, e)
		}
	}
	return out, nil
}

func entryMatchesKind(e *Entry, kind Kind) bool {
	return kind == "" || e.Kind == kind
}

func entryMatchesQuery(e *Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.Name), q) {
		return true
	}
	switch e.Kind {
	case KindServer:
		if e.Server == nil {
			return false
		}
		if strings.Contains(strings.ToLower(e.Server.Title), q) {
			return true
		}
		return strings.Contains(strings.ToLower(e.Server.Description), q)
	case KindSkill:
		if e.Skill == nil {
			return false
		}
		if strings.Contains(strings.ToLower(e.Skill.Title), q) {
			return true
		}
		return strings.Contains(strings.ToLower(e.Skill.Description), q)
	}
	return false
}

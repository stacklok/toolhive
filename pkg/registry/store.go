// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"fmt"
	"strings"
)

// Store is an in-memory Registry backed by a slice of entries indexed by
// name. It is the building block for registries that load their data once
// and serve queries from memory (file and URL sources).
//
// Registries that proxy every request to an upstream service should not use
// Store.
//
// Store is read-only after construction and safe for concurrent use.
type Store struct {
	name    string
	byName  map[string]*Entry
	ordered []*Entry
}

// NewStore returns a Store populated with the given entries.
//
// name is reported by Name(); it must be non-empty. Every entry is validated
// (see Entry.Validate); duplicate names cause an error rather than silent
// overwrite.
func NewStore(name string, entries []*Entry) (*Store, error) {
	if name == "" {
		return nil, errors.New("registry name must not be empty")
	}
	byName := make(map[string]*Entry, len(entries))
	ordered := make([]*Entry, 0, len(entries))
	for i, e := range entries {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("entries[%d]: %w", i, err)
		}
		if _, ok := byName[e.Name]; ok {
			return nil, fmt.Errorf("duplicate entry name %q in registry %q", e.Name, name)
		}
		byName[e.Name] = e
		ordered = append(ordered, e)
	}
	return &Store{
		name:    name,
		byName:  byName,
		ordered: ordered,
	}, nil
}

// Name returns the registry name supplied to NewStore.
func (s *Store) Name() string { return s.name }

// Get returns the entry with the given name, or ErrEntryNotFound.
func (s *Store) Get(name string) (*Entry, error) {
	if e, ok := s.byName[name]; ok {
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

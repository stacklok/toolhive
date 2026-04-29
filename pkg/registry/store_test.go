// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"testing"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	types "github.com/stacklok/toolhive-core/registry/types"
)

// fixtures

func serverEntry(name, title, description string) *Entry {
	return &Entry{
		Kind: KindServer,
		Name: name,
		Server: &v0.ServerJSON{
			Name:        name,
			Title:       title,
			Description: description,
		},
	}
}

func skillEntry(name, title, description string) *Entry {
	return &Entry{
		Kind: KindSkill,
		Name: name,
		Skill: &types.Skill{
			Name:        name,
			Title:       title,
			Description: description,
		},
	}
}

func TestNewStore(t *testing.T) {
	t.Parallel()

	t.Run("creates store with valid entries", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore("local", []*Entry{
			serverEntry("weather", "Weather", "Forecasts"),
			skillEntry("summarize", "Summarize", "Summarize text"),
		})
		require.NoError(t, err)
		assert.Equal(t, "local", s.Name())
	})

	t.Run("rejects empty name", func(t *testing.T) {
		t.Parallel()
		_, err := NewStore("", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "name must not be empty")
	})

	t.Run("rejects invalid entry", func(t *testing.T) {
		t.Parallel()
		_, err := NewStore("local", []*Entry{
			{Kind: KindServer, Name: "weather"}, // missing Server payload
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entries[0]")
	})

	t.Run("rejects duplicate names within same kind", func(t *testing.T) {
		t.Parallel()
		_, err := NewStore("local", []*Entry{
			serverEntry("weather", "", ""),
			serverEntry("weather", "", ""),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate")
	})

	t.Run("allows same name across different kinds", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore("local", []*Entry{
			serverEntry("io.github.user/semgrep", "", ""),
			skillEntry("io.github.user/semgrep", "", ""),
		})
		require.NoError(t, err)

		got, err := s.Get(KindServer, "io.github.user/semgrep")
		require.NoError(t, err)
		assert.Equal(t, KindServer, got.Kind)

		got, err = s.Get(KindSkill, "io.github.user/semgrep")
		require.NoError(t, err)
		assert.Equal(t, KindSkill, got.Kind)
	})

	t.Run("accepts empty entry list", func(t *testing.T) {
		t.Parallel()
		s, err := NewStore("local", nil)
		require.NoError(t, err)
		got, err := s.List(Filter{})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestStore_Get(t *testing.T) {
	t.Parallel()

	s, err := NewStore("local", []*Entry{
		serverEntry("weather", "Weather", "Forecasts"),
	})
	require.NoError(t, err)

	t.Run("returns existing entry", func(t *testing.T) {
		t.Parallel()
		e, err := s.Get(KindServer, "weather")
		require.NoError(t, err)
		assert.Equal(t, "weather", e.Name)
		assert.Equal(t, KindServer, e.Kind)
	})

	t.Run("returns ErrEntryNotFound for missing name", func(t *testing.T) {
		t.Parallel()
		_, err := s.Get(KindServer, "missing")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEntryNotFound))
	})

	t.Run("returns ErrEntryNotFound for wrong kind", func(t *testing.T) {
		t.Parallel()
		_, err := s.Get(KindSkill, "weather")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEntryNotFound))
	})
}

func TestStore_List(t *testing.T) {
	t.Parallel()

	s, err := NewStore("local", []*Entry{
		serverEntry("weather", "", ""),
		serverEntry("postgres", "", ""),
		skillEntry("summarize", "", ""),
	})
	require.NoError(t, err)

	t.Run("returns all entries with empty filter", func(t *testing.T) {
		t.Parallel()
		got, err := s.List(Filter{})
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})

	t.Run("filters by kind", func(t *testing.T) {
		t.Parallel()
		got, err := s.List(Filter{Kind: KindServer})
		require.NoError(t, err)
		assert.Len(t, got, 2)
		for _, e := range got {
			assert.Equal(t, KindServer, e.Kind)
		}

		got, err = s.List(Filter{Kind: KindSkill})
		require.NoError(t, err)
		assert.Len(t, got, 1)
		assert.Equal(t, KindSkill, got[0].Kind)
	})

	t.Run("preserves insertion order", func(t *testing.T) {
		t.Parallel()
		got, err := s.List(Filter{})
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, "weather", got[0].Name)
		assert.Equal(t, "postgres", got[1].Name)
		assert.Equal(t, "summarize", got[2].Name)
	})
}

func TestStore_Search(t *testing.T) {
	t.Parallel()

	s, err := NewStore("local", []*Entry{
		serverEntry("io.github.user/weather", "Weather", "Forecasts and warnings"),
		serverEntry("io.github.user/postgres", "Postgres", "PostgreSQL database server"),
		skillEntry("summarize", "Summarizer", "Condense long text"),
	})
	require.NoError(t, err)

	tests := []struct {
		name        string
		query       string
		filter      Filter
		expectNames []string
	}{
		{
			name:        "empty query returns all",
			query:       "",
			expectNames: []string{"io.github.user/weather", "io.github.user/postgres", "summarize"},
		},
		{
			name:        "matches against name",
			query:       "weather",
			expectNames: []string{"io.github.user/weather"},
		},
		{
			name:        "matches against title",
			query:       "summarizer",
			expectNames: []string{"summarize"},
		},
		{
			name:        "matches against description",
			query:       "database",
			expectNames: []string{"io.github.user/postgres"},
		},
		{
			name:        "case insensitive",
			query:       "POSTGRES",
			expectNames: []string{"io.github.user/postgres"},
		},
		{
			name:        "trims whitespace",
			query:       "  weather  ",
			expectNames: []string{"io.github.user/weather"},
		},
		{
			name:        "filter narrows results",
			query:       "",
			filter:      Filter{Kind: KindSkill},
			expectNames: []string{"summarize"},
		},
		{
			name:        "no matches returns empty",
			query:       "nonexistent",
			expectNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.Search(tt.query, tt.filter)
			require.NoError(t, err)
			names := make([]string, 0, len(got))
			for _, e := range got {
				names = append(names, e.Name)
			}
			assert.Equal(t, tt.expectNames, names)
		})
	}
}

// Store returned by NewStore must satisfy the Registry interface.
var _ Registry = (*Store)(nil)

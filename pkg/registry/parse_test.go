// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const upstreamWithServersAndSkills = `{
  "$schema": "https://example.com/schema.json",
  "version": "1.0.0",
  "meta": { "last_updated": "2026-01-01T00:00:00Z" },
  "data": {
    "servers": [
      {
        "$schema": "https://example.com/server.json",
        "name": "io.github.user/weather",
        "description": "Weather forecasts",
        "version": "1.0.0"
      },
      {
        "$schema": "https://example.com/server.json",
        "name": "io.github.user/postgres",
        "description": "PostgreSQL database access",
        "version": "0.5.1"
      }
    ],
    "skills": [
      {
        "namespace": "io.github.user",
        "name": "summarize",
        "description": "Condense long text",
        "version": "1.0.0"
      },
      {
        "name": "no-namespace",
        "description": "No namespace specified",
        "version": "0.1.0"
      }
    ]
  }
}`

const upstreamEmpty = `{
  "$schema": "https://example.com/schema.json",
  "version": "1.0.0",
  "meta": { "last_updated": "2026-01-01T00:00:00Z" },
  "data": { "servers": [] }
}`

const legacyFormat = `{
  "version": "1.0.0",
  "servers": {
    "weather": { "image": "weather:latest", "description": "weather" }
  }
}`

func TestParseEntries(t *testing.T) {
	t.Parallel()

	t.Run("decodes servers and skills", func(t *testing.T) {
		t.Parallel()
		entries, err := parseEntries([]byte(upstreamWithServersAndSkills))
		require.NoError(t, err)
		require.Len(t, entries, 4)

		// Servers come first, in document order.
		assert.Equal(t, KindServer, entries[0].Kind)
		assert.Equal(t, "io.github.user/weather", entries[0].Name)
		require.NotNil(t, entries[0].Server)
		assert.Equal(t, "Weather forecasts", entries[0].Server.Description)
		assert.Nil(t, entries[0].Skill)

		assert.Equal(t, KindServer, entries[1].Kind)
		assert.Equal(t, "io.github.user/postgres", entries[1].Name)

		// Skills follow.
		assert.Equal(t, KindSkill, entries[2].Kind)
		assert.Equal(t, "io.github.user/summarize", entries[2].Name)
		require.NotNil(t, entries[2].Skill)
		assert.Equal(t, "io.github.user", entries[2].Skill.Namespace)
		assert.Nil(t, entries[2].Server)

		// Skill without namespace uses the bare name.
		assert.Equal(t, KindSkill, entries[3].Kind)
		assert.Equal(t, "no-namespace", entries[3].Name)
	})

	t.Run("decodes empty data", func(t *testing.T) {
		t.Parallel()
		entries, err := parseEntries([]byte(upstreamEmpty))
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("rejects invalid JSON", func(t *testing.T) {
		t.Parallel()
		_, err := parseEntries([]byte("not json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse upstream registry")
	})

	t.Run("returns ErrLegacyFormat for legacy input", func(t *testing.T) {
		t.Parallel()
		_, err := parseEntries([]byte(legacyFormat))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrLegacyFormat),
			"want ErrLegacyFormat, got %v", err)
	})

	t.Run("output entries are valid", func(t *testing.T) {
		t.Parallel()
		entries, err := parseEntries([]byte(upstreamWithServersAndSkills))
		require.NoError(t, err)
		for i, e := range entries {
			assert.NoError(t, e.Validate(), "entries[%d] failed validation", i)
		}
	})
}

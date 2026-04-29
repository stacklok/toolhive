// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── mergeToolConfigs ──────────────────────────────────────────────────────────

func TestMergeToolConfigs_EmptyExisting(t *testing.T) {
	t.Parallel()
	incoming := []ToolConfig{{Tool: "claude-code", Mode: "direct", ConfigPath: "/a"}}
	got := mergeToolConfigs(nil, incoming)
	assert.Equal(t, incoming, got)
}

func TestMergeToolConfigs_AppendsNew(t *testing.T) {
	t.Parallel()
	existing := []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/c"}}
	incoming := []ToolConfig{{Tool: "claude-code", Mode: "direct", ConfigPath: "/a"}}
	got := mergeToolConfigs(existing, incoming)
	assert.Len(t, got, 2)
	assert.Equal(t, "cursor", got[0].Tool)
	assert.Equal(t, "claude-code", got[1].Tool)
}

func TestMergeToolConfigs_ReplacesExisting(t *testing.T) {
	t.Parallel()
	existing := []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/old"}}
	incoming := []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/new"}}
	got := mergeToolConfigs(existing, incoming)
	assert.Len(t, got, 1)
	assert.Equal(t, "/new", got[0].ConfigPath)
}

func TestMergeToolConfigs_MixedReplaceAndAppend(t *testing.T) {
	t.Parallel()
	existing := []ToolConfig{
		{Tool: "cursor", ConfigPath: "/old-cursor"},
		{Tool: "vscode", ConfigPath: "/old-vscode"},
	}
	incoming := []ToolConfig{
		{Tool: "cursor", ConfigPath: "/new-cursor"},
		{Tool: "claude-code", ConfigPath: "/claude"},
	}
	got := mergeToolConfigs(existing, incoming)
	assert.Len(t, got, 3)
	assert.Equal(t, "/new-cursor", got[0].ConfigPath)
	assert.Equal(t, "/old-vscode", got[1].ConfigPath)
	assert.Equal(t, "/claude", got[2].ConfigPath)
}

// ── isTarget ─────────────────────────────────────────────────────────────────

func TestIsTarget(t *testing.T) {
	t.Parallel()
	targets := []ToolConfig{
		{Tool: "claude-code"},
		{Tool: "cursor"},
	}
	assert.True(t, isTarget(targets, "claude-code"))
	assert.True(t, isTarget(targets, "cursor"))
	assert.False(t, isTarget(targets, "vscode"))
	assert.False(t, isTarget(targets, ""))
}

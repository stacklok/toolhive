// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"

	registrytypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/core"
)

// TestProfileFor guards the security boundary: the profile sent to an inference
// backend (potentially a remote LLM) must carry only public catalog metadata,
// never anything derived from the run config, permission profile, or secrets.
// ServerProfile having no such field is the structural guarantee; this test
// pins the mapping and documents the intent.
func TestProfileFor(t *testing.T) {
	t.Parallel()

	w := core.Workload{Name: "github", Remote: true}
	meta := &registrytypes.ImageMetadata{BaseServerMetadata: registrytypes.BaseServerMetadata{
		Description: "GitHub MCP server",
		Overview:    "Access repositories and issues",
		Tags:        []string{"git", "vcs"},
		Tools:       []string{"get_issue", "create_pr"},
	}}

	got := profileFor(w, meta)

	assert.Equal(t, ServerProfile{
		Name:        "github",
		Remote:      true,
		Description: "GitHub MCP server",
		Overview:    "Access repositories and issues",
		Tags:        []string{"git", "vcs"},
		Tools:       []string{"get_issue", "create_pr"},
	}, got)
}

func TestProfileForNilMetadata(t *testing.T) {
	t.Parallel()

	got := profileFor(core.Workload{Name: "local", Remote: false}, nil)
	assert.Equal(t, ServerProfile{Name: "local"}, got)
}

func TestMapAnnotations(t *testing.T) {
	t.Parallel()

	open := true
	ro := true
	tools := []mcp.Tool{
		{Name: "fetch", Annotations: mcp.ToolAnnotation{OpenWorldHint: &open}},
		{Name: "read", Annotations: mcp.ToolAnnotation{ReadOnlyHint: &ro}},
	}

	got := mapAnnotations(tools)

	assert.Len(t, got, 2)
	assert.NotNil(t, got["fetch"].OpenWorldHint)
	assert.True(t, *got["fetch"].OpenWorldHint)
	assert.NotNil(t, got["read"].ReadOnlyHint)
	assert.True(t, *got["read"].ReadOnlyHint)
	assert.Nil(t, got["read"].OpenWorldHint)
}

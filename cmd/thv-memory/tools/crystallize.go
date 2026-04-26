// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/memory"
)

// RegisterCrystallize registers the memory_crystallize tool.
func RegisterCrystallize(s *server.MCPServer, store memory.Store) {
	tool := mcp.NewTool("memory_crystallize",
		mcp.WithDescription("Generate a SKILL.md scaffold from procedural memory entries for human review and publishing."),
		mcp.WithArray("ids", mcp.Required(), mcp.Description("Array of procedural memory IDs"), mcp.WithStringItems()),
		mcp.WithString("name", mcp.Required(), mcp.Description("Proposed skill name (kebab-case)")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ids, err := req.RequireStringSlice("ids")
		if err != nil {
			return mcp.NewToolResultError("ids must be an array of strings"), nil
		}
		name := req.GetString("name", "")

		var contents []string
		for _, id := range ids {
			entry, err := store.Get(ctx, id)
			if err != nil {
				continue
			}
			contents = append(contents, entry.Content)
		}
		if len(contents) == 0 {
			return mcp.NewToolResultError("no valid entries found"), nil
		}

		scaffold := buildSkillScaffold(name, contents)
		out, _ := json.Marshal(map[string]string{
			"skill_name": name,
			"skill_md":   scaffold,
			"note":       "Review this scaffold, edit as needed, then publish with: thv skills push " + name,
		})
		return mcp.NewToolResultText(string(out)), nil
	})
}

func buildSkillScaffold(name string, contents []string) string {
	return fmt.Sprintf(`---
name: %s
description: "[TODO: one-line description of what this skill does]"
---

# %s

## Context

This skill was crystallized from %d procedural memory entries.

## Guidance

%s

## When to Use

[TODO: describe when an agent should apply this skill]
`, name, name, len(contents), "- "+strings.Join(contents, "\n- "))
}

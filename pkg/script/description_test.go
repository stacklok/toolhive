// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"strings"
	"testing"
)

func TestGenerateToolDescription(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{Name: "github-fetch-prs", Description: "Fetch pull requests from GitHub"},
		{Name: "slack-send-message", Description: "Send a message to a Slack channel"},
	}

	desc := GenerateToolDescription(tools)

	// Verify it contains tool names
	if !strings.Contains(desc, "github-fetch-prs") {
		t.Error("description should contain tool name github-fetch-prs")
	}
	if !strings.Contains(desc, "slack-send-message") {
		t.Error("description should contain tool name slack-send-message")
	}

	// Verify it contains tool descriptions
	if !strings.Contains(desc, "Fetch pull requests") {
		t.Error("description should contain tool description")
	}

	// Verify it mentions parallel and call_tool
	if !strings.Contains(desc, "parallel") {
		t.Error("description should mention parallel()")
	}
	if !strings.Contains(desc, "call_tool") {
		t.Error("description should mention call_tool()")
	}
}

func TestGenerateToolDescription_TruncatesLongDescriptions(t *testing.T) {
	t.Parallel()

	longDesc := strings.Repeat("a", 100)
	tools := []Tool{
		{Name: "my-tool", Description: longDesc},
	}

	desc := GenerateToolDescription(tools)

	if strings.Contains(desc, longDesc) {
		t.Error("description should truncate long tool descriptions")
	}
	if !strings.Contains(desc, "...") {
		t.Error("truncated description should end with ellipsis")
	}
}

func TestGenerateToolDescription_Empty(t *testing.T) {
	t.Parallel()

	desc := GenerateToolDescription(nil)

	if !strings.Contains(desc, "parallel") {
		t.Error("empty tool list should still describe builtins")
	}
}

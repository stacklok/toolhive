// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testBuiltinCallTool = "call_tool"
	testScriptReturnOne = "return 1"
)

func TestGenerateToolDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tools    []Tool
		contains []string
	}{
		{
			name: "lists tools and builtins",
			tools: []Tool{
				{Name: "github-fetch-prs", Description: "Fetch pull requests from GitHub"},
				{Name: "slack-send-message", Description: "Send a message to a Slack channel"},
			},
			contains: []string{
				"github-fetch-prs", "slack-send-message",
				"Fetch pull requests", "parallel", testBuiltinCallTool,
			},
		},
		{
			name:  "truncates long descriptions",
			tools: []Tool{{Name: "my-tool", Description: strings.Repeat("a", 100)}},
			contains: []string{
				"my-tool", "...",
			},
		},
		{
			name:     "empty tool list still describes builtins",
			tools:    nil,
			contains: []string{"parallel", testBuiltinCallTool},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			desc := GenerateToolDescription(tt.tools)
			for _, s := range tt.contains {
				require.Contains(t, desc, s)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokens

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestCountToolTokens(t *testing.T) {
	t.Parallel()
	counter := NewCounter()

	tool := mcp.Tool{
		Name:        "test_tool",
		Description: "A test tool for counting tokens",
	}

	tokens := counter.CountToolTokens(tool)

	// Should return a positive number
	if tokens <= 0 {
		t.Errorf("Expected positive token count, got %d", tokens)
	}

	// Rough estimate: tool should have at least a few tokens
	if tokens < 5 {
		t.Errorf("Expected at least 5 tokens for a tool with name and description, got %d", tokens)
	}
}

func TestCountToolTokens_MinimalTool(t *testing.T) {
	t.Parallel()
	counter := NewCounter()

	// Minimal tool with just a name
	tool := mcp.Tool{
		Name: "minimal",
	}

	tokens := counter.CountToolTokens(tool)

	// Should return a positive number even for minimal tool
	if tokens <= 0 {
		t.Errorf("Expected positive token count for minimal tool, got %d", tokens)
	}
}

func TestCountToolTokens_NoDescription(t *testing.T) {
	t.Parallel()
	counter := NewCounter()

	tool := mcp.Tool{
		Name: "test_tool",
	}

	tokens := counter.CountToolTokens(tool)

	// Should still return a positive number
	if tokens <= 0 {
		t.Errorf("Expected positive token count for tool without description, got %d", tokens)
	}
}

func TestCountToolsTokens(t *testing.T) {
	t.Parallel()
	counter := NewCounter()

	tools := []mcp.Tool{
		{
			Name:        "tool1",
			Description: "First tool",
		},
		{
			Name:        "tool2",
			Description: "Second tool with longer description",
		},
	}

	totalTokens := counter.CountToolsTokens(tools)

	// Should be greater than individual tools
	tokens1 := counter.CountToolTokens(tools[0])
	tokens2 := counter.CountToolTokens(tools[1])

	expectedTotal := tokens1 + tokens2
	if totalTokens != expectedTotal {
		t.Errorf("Expected total tokens %d, got %d", expectedTotal, totalTokens)
	}
}

func TestCountToolsTokens_EmptyList(t *testing.T) {
	t.Parallel()
	counter := NewCounter()

	tokens := counter.CountToolsTokens([]mcp.Tool{})

	// Should return 0 for empty list
	if tokens != 0 {
		t.Errorf("Expected 0 tokens for empty list, got %d", tokens)
	}
}

func TestEstimateText(t *testing.T) {
	t.Parallel()
	counter := NewCounter()

	tests := []struct {
		name string
		text string
		want int
	}{
		{
			name: "Empty text",
			text: "",
			want: 0,
		},
		{
			name: "Short text",
			text: "Hello",
			want: 1, // 5 chars / 4 chars per token ≈ 1
		},
		{
			name: "Medium text",
			text: "This is a test message",
			want: 5, // 22 chars / 4 chars per token ≈ 5
		},
		{
			name: "Long text",
			text: "This is a much longer test message that should have more tokens because it contains significantly more characters",
			want: 28, // 112 chars / 4 chars per token = 28
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := counter.EstimateText(tt.text)
			if got != tt.want {
				t.Errorf("EstimateText() = %v, want %v", got, tt.want)
			}
		})
	}
}

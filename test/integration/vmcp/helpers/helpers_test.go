package helpers

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

// TestGetToolNames tests the GetToolNames helper function.
func TestGetToolNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		result   *mcp.ListToolsResult
		expected []string
	}{
		{
			name: "empty tools",
			result: &mcp.ListToolsResult{
				Tools: []mcp.Tool{},
			},
			expected: []string{},
		},
		{
			name: "single tool",
			result: &mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "tool1"},
				},
			},
			expected: []string{"tool1"},
		},
		{
			name: "multiple tools",
			result: &mcp.ListToolsResult{
				Tools: []mcp.Tool{
					{Name: "tool1"},
					{Name: "tool2"},
					{Name: "tool3"},
				},
			},
			expected: []string{"tool1", "tool2", "tool3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			names := GetToolNames(tt.result)
			assert.Equal(t, tt.expected, names)
		})
	}
}

// TestAssertTextContains tests the AssertTextContains helper.
func TestAssertTextContains(t *testing.T) {
	t.Parallel()
	t.Run("all substrings present", func(t *testing.T) {
		t.Parallel()
		text := "hello world, this is a test"
		// Should not fail
		AssertTextContains(t, text, "hello", "world", "test")
	})
}

// TestAssertTextNotContains tests the AssertTextNotContains helper.
func TestAssertTextNotContains(t *testing.T) {
	t.Parallel()
	t.Run("no forbidden substrings", func(t *testing.T) {
		t.Parallel()
		text := "hello world"
		// Should not fail
		AssertTextNotContains(t, text, "password", "secret")
	})
}

package tokens

import (
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// Counter counts tokens for LLM consumption
// This provides estimates of token usage for tools
type Counter struct {
	// Simple heuristic: ~4 characters per token for English text
	charsPerToken float64
}

// NewCounter creates a new token counter
func NewCounter() *Counter {
	return &Counter{
		charsPerToken: 4.0, // GPT-style tokenization approximation
	}
}

// CountToolTokens estimates the number of tokens for a tool
func (c *Counter) CountToolTokens(tool mcp.Tool) int {
	// Convert tool to JSON representation (as it would be sent to LLM)
	toolJSON, err := json.Marshal(tool)
	if err != nil {
		// Fallback to simple estimation
		return c.estimateFromTool(tool)
	}

	// Estimate tokens from JSON length
	return int(float64(len(toolJSON)) / c.charsPerToken)
}

// estimateFromTool provides a fallback estimation from tool fields
func (c *Counter) estimateFromTool(tool mcp.Tool) int {
	totalChars := len(tool.Name)
	
	if tool.Description != "" {
		totalChars += len(tool.Description)
	}
	
	// Estimate input schema size
	schemaJSON, _ := json.Marshal(tool.InputSchema)
	totalChars += len(schemaJSON)
	
	return int(float64(totalChars) / c.charsPerToken)
}

// CountToolsTokens calculates total tokens for multiple tools
func (c *Counter) CountToolsTokens(tools []mcp.Tool) int {
	total := 0
	for _, tool := range tools {
		total += c.CountToolTokens(tool)
	}
	return total
}

// EstimateText estimates tokens for arbitrary text
func (c *Counter) EstimateText(text string) int {
	return int(float64(len(text)) / c.charsPerToken)
}


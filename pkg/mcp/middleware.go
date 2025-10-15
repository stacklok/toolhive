package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Middleware type constants
const (
	ParserMiddlewareType         = "mcp-parser"
	ToolFilterMiddlewareType     = "tool-filter"
	ToolCallFilterMiddlewareType = "tool-call-filter"
)

// ParserMiddlewareParams represents the parameters for MCP parser middleware
type ParserMiddlewareParams struct {
	// No parameters needed for MCP parser
}

// ToolOverride represents a tool override entry.
type ToolOverride struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ToolFilterMiddlewareParams represents the parameters for tool filter middleware
type ToolFilterMiddlewareParams struct {
	FilterTools   []string                `json:"filter_tools"`
	ToolsOverride map[string]ToolOverride `json:"tools_override"`
}

// ParserMiddleware wraps MCP parser middleware functionality
type ParserMiddleware struct{}

// Handler returns the middleware function used by the proxy.
func (*ParserMiddleware) Handler() types.MiddlewareFunction {
	return ParsingMiddleware
}

// Close cleans up any resources used by the middleware.
func (*ParserMiddleware) Close() error {
	// MCP parser middleware doesn't need cleanup
	return nil
}

// ToolFilterMiddleware wraps tool filter middleware functionality
type ToolFilterMiddleware struct {
	middleware types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *ToolFilterMiddleware) Handler() types.MiddlewareFunction {
	return m.middleware
}

// Close cleans up any resources used by the middleware.
func (*ToolFilterMiddleware) Close() error {
	// Tool filter middleware doesn't need cleanup
	return nil
}

// CreateParserMiddleware factory function for MCP parser middleware
func CreateParserMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {

	mcpMw := &ParserMiddleware{}
	runner.AddMiddleware(config.Type, mcpMw)
	return nil
}

// CreateToolFilterMiddleware factory function for tool filter middleware
func CreateToolFilterMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {

	var params ToolFilterMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal tool filter middleware parameters: %w", err)
	}

	opts := []ToolMiddlewareOption{}
	opts = append(opts, WithToolsFilter(params.FilterTools...))
	for actualName, tool := range params.ToolsOverride {
		opts = append(opts, WithToolsOverride(actualName, tool.Name, tool.Description))
	}

	middleware, err := NewListToolsMappingMiddleware(opts...)
	if err != nil {
		return fmt.Errorf("failed to create tool filter middleware: %w", err)
	}

	toolFilterMw := &ToolFilterMiddleware{middleware: middleware}
	runner.AddMiddleware(config.Type, toolFilterMw)
	return nil
}

// CreateToolCallFilterMiddleware factory function for tool call filter middleware
func CreateToolCallFilterMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {

	var params ToolFilterMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal tool call filter middleware parameters: %w", err)
	}

	opts := []ToolMiddlewareOption{}
	opts = append(opts, WithToolsFilter(params.FilterTools...))
	for actualName, tool := range params.ToolsOverride {
		opts = append(opts, WithToolsOverride(actualName, tool.Name, tool.Description))
	}

	middleware, err := NewToolCallMappingMiddleware(opts...)
	if err != nil {
		return fmt.Errorf("failed to create tool call filter middleware: %w", err)
	}

	toolCallFilterMw := &ToolFilterMiddleware{middleware: middleware}
	runner.AddMiddleware(config.Type, toolCallFilterMw)
	return nil
}

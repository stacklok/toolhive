// Package testkit provides testing utilities for ToolHive.
//
// Its sole purpose is
//
//   - providing utilities to quickly spin-up an HTTP test server exposing
//     either a Streamable-HTTP or (legacy) SSE MCP server
//   - providing utilities to ease the parsing of `text/event-stream` response
//     bodies
//
// The file `pkg/testkit/testkit_test.go` contains a few tests that
// exemplify how to use the framework. Ideally, it should allow the
// developer to add assertions in the test server as well, but for
// now it only allows configuring the returned JSON payloads.
package testkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

const (
	toolsListMethod = "tools/list"
	toolsCallMethod = "tools/call"
)

type clientType string

const (
	clientTypeJSON clientType = "application/json"
	clientTypeSSE  clientType = "text/event-stream"
)

// TestMCPClient is the common interface that test MCP clients must implement.
// Client implementations are expected to abstract the underlying transport so
// that responses coming from the same TCP stream or from different ones are
// treated the same.
type TestMCPClient interface {
	// ToolsList returns the tools list response for the client.
	// Client implementations are expected to strip any non-JSON payloads
	// from the response, i.e. just return the JSON payload after a
	// `data:` prefix.
	ToolsList() ([]byte, error)
	// ToolsCall returns the tool call response for the client.
	// Client implementations are expected to strip any non-JSON payloads
	// from the response, i.e. just return the JSON payload after a
	// `data:` prefix.
	ToolsCall(name string) ([]byte, error)
}

// TestMCPServer is the common interface that test MCP servers must implement.
// This allows having a single set of options for all test MCP servers,
// regardless of the underlying implementation.
type TestMCPServer interface {
	SetMiddlewares(middlewares ...func(http.Handler) http.Handler) error
	AddTool(tool tooldef) error
	SetClientType(clientType clientType) error
}

// TestMCPServerOption is a function that can be used to configure a test MCP server.
// It uses the TestMCPServer interface to configure the server.
type TestMCPServerOption func(TestMCPServer) error

// WithMiddlewares is a function that can be used to configure a test MCP server with middlewares.
// The actual order of application of the middleware functions is determined by the server
// implementation, but is generally expected to be the same as the one provided.
func WithMiddlewares(middlewares ...func(http.Handler) http.Handler) TestMCPServerOption {
	return func(s TestMCPServer) error {
		return s.SetMiddlewares(middlewares...)
	}
}

type tooldef struct {
	Name        string
	Description string
	Handler     func() string
}

// WithTool is a function that can be used to configure a test MCP server with a tool.
// The underlying implementation is expected to honor this and return the tool
// as part of the tools list response, as well as handle tool call requests using the given
// handler function.
func WithTool(name string, description string, handler func() string) TestMCPServerOption {
	return func(s TestMCPServer) error {
		return s.AddTool(tooldef{
			Name:        name,
			Description: description,
			Handler:     handler,
		})
	}
}

// WithJSONClientType configures the test MCP server to provide a client calling
// endpoints that return application/json responses.
func WithJSONClientType() TestMCPServerOption {
	return func(s TestMCPServer) error {
		return s.SetClientType(clientTypeJSON)
	}
}

// WithSSEClientType configures the test MCP server to provide a client calling
// endpoints that return text/event-stream responses.
func WithSSEClientType() TestMCPServerOption {
	return func(s TestMCPServer) error {
		return s.SetClientType(clientTypeSSE)
	}
}

// SSESep is a type that represents the separator for SSE responses.
type SSESep int

const (
	// LFSep is the line feed separator for SSE responses.
	LFSep = iota
	// CRSep is the carriage return separator for SSE responses.
	CRSep
	// CRLFSep is the carriage return line feed separator for SSE responses.
	CRLFSep
)

// NewSplitSSE is a function that can be used to create a new SSE split function.
// It's just a helper function to be used with bufio.Scanner.Split.
func NewSplitSSE(sep SSESep) bufio.SplitFunc {
	var separator []byte

	switch sep {
	case LFSep:
		separator = []byte("\n\n")
	case CRSep:
		separator = []byte("\r\r")
	case CRLFSep:
		separator = []byte("\r\n\r\n")
	}

	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		if i := bytes.Index(data, separator); i >= 0 {
			return i + 2, data[0:i], nil
		}

		return 0, nil, nil
	}
}

func makeToolsList(tools map[string]tooldef) string {
	toolsList := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		toolsList = append(toolsList, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
		})
	}

	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"tools": toolsList,
		},
	}

	response, err := json.Marshal(res)
	if err != nil {
		return fmt.Sprintf("failed to marshal tools list: %v", err)
	}

	return string(response)
}

func runToolCall(tools map[string]tooldef, mcpRequest map[string]any) string {
	params, ok := mcpRequest["params"].(map[string]any)
	if !ok {
		return simpleError(fmt.Sprintf("failed to get tool params: %v", mcpRequest))
	}

	toolName, ok := params["name"].(string)
	if !ok {
		return simpleError(fmt.Sprintf("failed to get tool name: %v", mcpRequest))
	}

	if _, ok := tools[toolName]; !ok {
		return simpleError(fmt.Sprintf("tool %s not found", toolName))
	}

	text := tools[toolName].Handler()
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		},
	}

	payload, err := json.Marshal(res)
	if err != nil {
		return simpleError(fmt.Sprintf("failed to marshal tool call: %v", err))
	}

	return string(payload)
}

func simpleError(message string) string {
	res := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"error":   map[string]any{"message": message},
	}

	payload, err := json.Marshal(res)
	if err != nil {
		return fmt.Sprintf("failed to marshal simple error: %v", err)
	}

	return string(payload)
}

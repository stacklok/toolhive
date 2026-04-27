// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/core"
)

// inspSpinFrames holds the spinner animation frames for the inspector loading state.
var inspSpinFrames = []string{"⠋", "⠙", "⠸", "⠴", "⠦", "⠧", "⠇", "⠏"}

// inspCallResultMsg is sent when a tool call completes in the inspector.
type inspCallResultMsg struct {
	result    *mcp.CallToolResult
	elapsedMs int64
	err       error
}

// inspSpinTickMsg is sent on each spinner tick while loading.
type inspSpinTickMsg struct{}

// buildInspFields parses a tool's InputSchema and returns form fields.
// It extracts properties from the JSON Schema and creates textinput models.
func buildInspFields(tool mcp.Tool) []formField {
	props := tool.InputSchema.Properties
	if props == nil {
		return nil
	}

	reqSet := buildRequiredSetFromSlice(tool.InputSchema.Required)

	// Iterate in a stable order by collecting keys first.
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	var fields []formField
	for _, name := range keys {
		def, ok := props[name].(map[string]any)
		if !ok {
			continue
		}

		fieldType, _ := def["type"].(string)
		if fieldType == "" {
			fieldType = "string"
		}
		desc, _ := def["description"].(string)

		ti := textinput.New()
		ti.Placeholder = fieldType
		ti.Width = 40

		fields = append(fields, formField{
			input:    ti,
			name:     name,
			required: reqSet[name],
			desc:     desc,
			typeName: fieldType,
		})
	}

	return fields
}

// buildRequiredSetFromSlice returns a set of required field names from a string slice.
func buildRequiredSetFromSlice(required []string) map[string]bool {
	reqSet := map[string]bool{}
	for _, s := range required {
		reqSet[s] = true
	}
	return reqSet
}

// shellEscapeSingleQuote escapes single quotes for safe inclusion inside
// a single-quoted shell string: ' → '"'"' (end quote, escaped quote, reopen).
func shellEscapeSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", `'"'"'`)
}

// buildCurlStr constructs a curl command string for the given tool call.
func buildCurlStr(sel *core.Workload, toolName string, args map[string]any) string {
	if sel == nil {
		return ""
	}

	url := sel.URL
	if url == "" {
		url = fmt.Sprintf("http://127.0.0.1:%d/sse", sel.Port)
	}

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		payloadJSON = []byte("{}")
	}

	return fmt.Sprintf("curl -X POST \\\n  '%s' \\\n  -H 'Content-Type: application/json' \\\n  -d '%s'",
		shellEscapeSingleQuote(url), shellEscapeSingleQuote(string(payloadJSON)))
}

// startInspCallTool returns a tea.Cmd that calls a tool asynchronously.
func startInspCallTool(ctx context.Context, c *mcpclient.Client, toolName string, args map[string]any) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		result, err := callTool(ctx, c, toolName, args)
		elapsed := time.Since(start).Milliseconds()
		return inspCallResultMsg{result: result, elapsedMs: elapsed, err: err}
	}
}

// callTool invokes a tool on the backend MCP server via an already-connected client.
func callTool(ctx context.Context, c *mcpclient.Client, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args
	return c.CallTool(ctx, req)
}

// inspFieldValues builds a map[string]any from current form field input values,
// coercing each value to the type declared in the tool's JSON Schema.
// Required fields that are empty produce an error. Empty optional fields are skipped.
// On error, errIdx is the index of the offending field (or -1 if unknown).
func inspFieldValues(fields []formField) (map[string]any, int, error) {
	result := make(map[string]any)
	for i, f := range fields {
		v := strings.TrimSpace(f.input.Value())
		if v == "" {
			if f.required {
				return nil, i, fmt.Errorf("field %q is required", f.name)
			}
			continue
		}
		parsed, err := parseFieldValue(v, f.typeName)
		if err != nil {
			return nil, i, fmt.Errorf("field %q: %w", f.name, err)
		}
		result[f.name] = parsed
	}
	return result, -1, nil
}

// parseFieldValue converts a string input to the Go type matching the given
// JSON Schema type name. Unknown types default to string.
func parseFieldValue(v, typeName string) (any, error) {
	switch typeName {
	case "integer":
		return strconv.ParseInt(v, 10, 64)
	case "number":
		return strconv.ParseFloat(v, 64)
	case "boolean":
		return strconv.ParseBool(v)
	case "array", "object":
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return parsed, nil
	default:
		return v, nil
	}
}

// formatInspResult formats a CallToolResult as a pretty-printed JSON string.
func formatInspResult(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, c := range result.Content {
		switch tc := c.(type) {
		case mcp.TextContent:
			parts = append(parts, tc.Text)
		default:
			b, _ := json.MarshalIndent(c, "", "  ")
			parts = append(parts, string(b))
		}
	}
	if len(parts) == 0 {
		b, _ := json.MarshalIndent(result, "", "  ")
		return string(b)
	}
	return strings.Join(parts, "\n")
}

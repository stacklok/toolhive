// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// inspSpinFrames holds the spinner animation frames for the inspector loading state.
var inspSpinFrames = []string{"⠋", "⠙", "⠸", "⠴", "⠦", "⠧", "⠇", "⠏"}

// inspCallResultMsg is sent when a tool call completes in the inspector.
type inspCallResultMsg struct {
	result    *vmcp.ToolCallResult
	elapsedMs int64
	err       error
}

// inspSpinTickMsg is sent on each spinner tick while loading.
type inspSpinTickMsg struct{}

// buildInspFields parses a tool's InputSchema and returns form fields.
// It extracts properties from the JSON Schema and creates textinput models.
func buildInspFields(tool vmcp.Tool) []formField {
	props, _ := tool.InputSchema["properties"].(map[string]any)
	if props == nil {
		return nil
	}

	reqSet := buildRequiredSet(tool.InputSchema)

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
		if reqSet[name] {
			ti.Placeholder = fieldType + " (required)"
		}
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

// buildRequiredSet returns a set of required field names from a JSON Schema.
func buildRequiredSet(schema map[string]any) map[string]bool {
	reqSet := map[string]bool{}
	reqArr, _ := schema["required"].([]any)
	for _, r := range reqArr {
		if s, ok := r.(string); ok {
			reqSet[s] = true
		}
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
func startInspCallTool(ctx context.Context, sel *core.Workload, toolName string, args map[string]any) tea.Cmd {
	wCopy := *sel
	return func() tea.Msg {
		start := time.Now()
		result, err := callTool(ctx, &wCopy, toolName, args)
		elapsed := time.Since(start).Milliseconds()
		return inspCallResultMsg{result: result, elapsedMs: elapsed, err: err}
	}
}

// callTool invokes a tool on the backend MCP server.
func callTool(ctx context.Context, workload *core.Workload, toolName string, args map[string]any) (*vmcp.ToolCallResult, error) {
	mcpClient, target, err := newBackendClientAndTarget(ctx, workload)
	if err != nil {
		return nil, err
	}

	return mcpClient.CallTool(ctx, target, toolName, args, nil)
}

// inspFieldValues builds a map[string]any from current form field input values, skipping empty fields.
func inspFieldValues(fields []formField) map[string]any {
	result := make(map[string]any)
	for _, f := range fields {
		v := strings.TrimSpace(f.input.Value())
		if v == "" {
			continue
		}
		result[f.name] = v
	}
	return result
}

// formatInspResult formats a ToolCallResult as a pretty-printed JSON string.
func formatInspResult(result *vmcp.ToolCallResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, c := range result.Content {
		switch c.Type {
		case vmcp.ContentTypeText:
			parts = append(parts, c.Text)
		case vmcp.ContentTypeImage, vmcp.ContentTypeAudio, vmcp.ContentTypeResource, vmcp.ContentTypeLink:
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

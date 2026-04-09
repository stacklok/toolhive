// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"go.starlark.net/starlark"
)

// CallToolResult holds the result of an MCP tool call.
type CallToolResult struct {
	Content           []ContentItem          `json:"content"`
	StructuredContent map[string]interface{} `json:"structuredContent,omitempty"`
	IsError           bool                   `json:"isError,omitempty"`
}

// ContentItem represents a single content item in an MCP tool result.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCaller is the interface for making MCP tool calls.
type ToolCaller interface {
	CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*CallToolResult, error)
}

// ToolInfo describes an MCP tool available to scripts.
type ToolInfo struct {
	Name        string
	Description string
}

// BuildGlobals creates Starlark globals from MCP tools, a caller, data arguments, and a context.
// Each tool becomes a callable Starlark function. A generic call_tool() builtin is also provided.
func BuildGlobals(ctx context.Context, tools []ToolInfo, caller ToolCaller, data map[string]interface{}) starlark.StringDict {
	globals := make(starlark.StringDict)

	// Track sanitized names for collision detection
	seen := make(map[string]string) // sanitized → original

	for _, tool := range tools {
		sanitized := sanitizeToolName(tool.Name)
		if existing, ok := seen[sanitized]; ok {
			slog.Warn("tool name collision after sanitization",
				"tool1", existing, "tool2", tool.Name, "sanitized", sanitized)
			continue
		}
		seen[sanitized] = tool.Name
		globals[sanitized] = makeToolBuiltin(ctx, tool.Name, sanitized, caller)
	}

	// Generic call_tool builtin for tools with awkward names
	globals["call_tool"] = starlark.NewBuiltin("call_tool", func(
		_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("call_tool: requires at least 1 positional argument (tool name)")
		}
		nameVal, ok := args[0].(starlark.String)
		if !ok {
			return nil, fmt.Errorf("call_tool: first argument must be a string, got %s", args[0].Type())
		}
		toolName := string(nameVal)
		arguments := kwargsToGoMap(kwargs)
		return callToolAndConvert(ctx, caller, toolName, arguments)
	})

	// parallel() builtin — execute a list of callables concurrently
	globals["parallel"] = starlark.NewBuiltin("parallel", parallelBuiltin)

	// Inject data arguments as top-level globals
	for k, v := range data {
		sv, err := goToStarlark(v)
		if err != nil {
			slog.Warn("failed to convert data argument to Starlark", "key", k, "error", err)
			continue
		}
		globals[k] = sv
	}

	return globals
}

func makeToolBuiltin(ctx context.Context, realName, displayName string, caller ToolCaller) *starlark.Builtin {
	return starlark.NewBuiltin(displayName, func(
		_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if len(args) > 0 {
			return nil, fmt.Errorf("%s: use keyword arguments only (e.g., %s(key=value))", displayName, displayName)
		}
		arguments := kwargsToGoMap(kwargs)
		return callToolAndConvert(ctx, caller, realName, arguments)
	})
}

func callToolAndConvert(
	ctx context.Context, caller ToolCaller, toolName string, arguments map[string]interface{},
) (starlark.Value, error) {
	result, err := caller.CallTool(ctx, toolName, arguments)
	if err != nil {
		return nil, fmt.Errorf("tool %q call failed: %w", toolName, err)
	}

	goVal, err := parseToolResult(result)
	if err != nil {
		return nil, fmt.Errorf("tool %q returned error: %w", toolName, err)
	}

	sv, err := goToStarlark(goVal)
	if err != nil {
		return nil, fmt.Errorf("tool %q result conversion failed: %w", toolName, err)
	}
	return sv, nil
}

func kwargsToGoMap(kwargs []starlark.Tuple) map[string]interface{} {
	m := make(map[string]interface{}, len(kwargs))
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		m[key] = starlarkToGo(kv[1])
	}
	return m
}

// parseToolResult converts a CallToolResult into a Go value.
func parseToolResult(result *CallToolResult) (interface{}, error) {
	if result.IsError {
		msg := "tool execution error"
		if len(result.Content) > 0 && result.Content[0].Text != "" {
			msg = result.Content[0].Text
		}
		return nil, fmt.Errorf("%s", msg)
	}

	// Prefer structured content, but unwrap the common SDK wrapper
	// pattern where the result is {"result": <actual value>}.
	if result.StructuredContent != nil {
		if len(result.StructuredContent) == 1 {
			if v, ok := result.StructuredContent["result"]; ok {
				return v, nil
			}
		}
		return result.StructuredContent, nil
	}

	// Fall back to parsing first text content as JSON
	if len(result.Content) == 0 {
		return nil, nil
	}

	if len(result.Content) > 1 {
		slog.Debug("tool returned multiple content items, using first text item only",
			"count", len(result.Content))
	}

	text := result.Content[0].Text

	var parsed interface{}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		// Not valid JSON — return as plain string
		return text, nil
	}
	return parsed, nil
}

// parallelBuiltin executes a list of zero-arg callables concurrently and
// returns a list of results where result[i] corresponds to callable[i].
func parallelBuiltin(
	thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple,
) (starlark.Value, error) {
	var fns *starlark.List
	if err := starlark.UnpackPositionalArgs("parallel", args, kwargs, 1, &fns); err != nil {
		return nil, err
	}

	n := fns.Len()
	if n == 0 {
		return starlark.NewList(nil), nil
	}

	results := make([]starlark.Value, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	wg.Add(n)

	for i := range n {
		go func(idx int) {
			defer wg.Done()

			callable, ok := fns.Index(idx).(starlark.Callable)
			if !ok {
				errs[idx] = fmt.Errorf("parallel: element %d is not callable (got %s)",
					idx, fns.Index(idx).Type())
				return
			}

			childThread := &starlark.Thread{
				Name:  fmt.Sprintf("%s/parallel-%d", thread.Name, idx),
				Print: thread.Print,
			}

			result, err := starlark.Call(childThread, callable, nil, nil)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = result
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("parallel: task %d failed: %w", i, err)
		}
	}

	return starlark.NewList(results), nil
}

var nonIdentChar = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// sanitizeToolName converts an MCP tool name into a valid Starlark identifier.
func sanitizeToolName(name string) string {
	s := nonIdentChar.ReplaceAllString(name, "_")
	if len(s) > 0 && unicode.IsDigit(rune(s[0])) {
		s = "_" + s
	}
	if s == "" {
		s = "_"
	}
	return s
}

// GenerateToolDescription creates a dynamic description for the execute_tool_script tool.
func GenerateToolDescription(tools []ToolInfo) string {
	var b strings.Builder
	b.WriteString("Execute a Starlark script that orchestrates multiple tool calls ")
	b.WriteString("and returns an aggregated result. Use 'return' to produce output.\n\n")
	b.WriteString("Available tools (callable as functions with keyword arguments):\n")
	for _, t := range tools {
		sanitized := sanitizeToolName(t.Name)
		desc := t.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		fmt.Fprintf(&b, "  - %s: %s\n", sanitized, desc)
	}
	b.WriteString("\nTool names with special characters are available with underscores ")
	b.WriteString("(e.g., my-tool becomes my_tool). Use call_tool(\"name\", ...) for any tool by its original name.\n\n")
	b.WriteString("Built-in: parallel([fn1, fn2, ...]) executes zero-arg callables concurrently ")
	b.WriteString("and returns results in order. Use with lambda to fan out tool calls.\n\n")
	b.WriteString("Named data arguments passed in the 'data' parameter are available as top-level variables in the script.")
	return b.String()
}

// starlarkToGo converts a Starlark value to a Go value.
func starlarkToGo(v starlark.Value) interface{} {
	switch v := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(v)
	case starlark.Int:
		if i, ok := v.Int64(); ok {
			return i
		}
		return v.String()
	case starlark.Float:
		return float64(v)
	case starlark.String:
		return string(v)
	case *starlark.List:
		result := make([]interface{}, v.Len())
		for i := 0; i < v.Len(); i++ {
			result[i] = starlarkToGo(v.Index(i))
		}
		return result
	case *starlark.Dict:
		result := make(map[string]interface{})
		for _, item := range v.Items() {
			key := starlarkToGo(item[0])
			keyStr, ok := key.(string)
			if !ok {
				keyStr = fmt.Sprintf("%v", key)
			}
			result[keyStr] = starlarkToGo(item[1])
		}
		return result
	case starlark.Tuple:
		result := make([]interface{}, len(v))
		for i, elem := range v {
			result[i] = starlarkToGo(elem)
		}
		return result
	default:
		return v.String()
	}
}

// goToStarlark converts a Go value to a Starlark value.
//
//nolint:gocyclo // type switch over Go types is inherently branchy
func goToStarlark(v interface{}) (starlark.Value, error) {
	switch v := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(v), nil
	case int:
		return starlark.MakeInt(v), nil
	case int64:
		return starlark.MakeInt64(v), nil
	case float64:
		return goFloat64ToStarlark(v), nil
	case string:
		return starlark.String(v), nil
	case []interface{}:
		return goSliceToStarlark(v)
	case map[string]interface{}:
		return goMapToStarlark(v)
	case json.Number:
		return goJSONNumberToStarlark(v)
	default:
		return nil, fmt.Errorf("unsupported Go type %T for Starlark conversion", v)
	}
}

func goFloat64ToStarlark(v float64) starlark.Value {
	if v == math.Trunc(v) && !math.IsInf(v, 0) && !math.IsNaN(v) && math.Abs(v) < (1<<53) {
		return starlark.MakeInt64(int64(v))
	}
	return starlark.Float(v)
}

func goSliceToStarlark(v []interface{}) (starlark.Value, error) {
	elems := make([]starlark.Value, len(v))
	for i, e := range v {
		sv, err := goToStarlark(e)
		if err != nil {
			return nil, err
		}
		elems[i] = sv
	}
	return starlark.NewList(elems), nil
}

func goMapToStarlark(v map[string]interface{}) (starlark.Value, error) {
	d := starlark.NewDict(len(v))
	for k, val := range v {
		sv, err := goToStarlark(val)
		if err != nil {
			return nil, err
		}
		if err := d.SetKey(starlark.String(k), sv); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func goJSONNumberToStarlark(v json.Number) (starlark.Value, error) {
	if i, err := v.Int64(); err == nil {
		return starlark.MakeInt64(i), nil
	}
	if f, err := v.Float64(); err == nil {
		return starlark.Float(f), nil
	}
	return starlark.String(v.String()), nil
}

// ResultToJSON converts a Starlark value to a JSON string.
func ResultToJSON(v starlark.Value) (string, error) {
	goVal := starlarkToGo(v)
	b, err := json.Marshal(goVal)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result to JSON: %w", err)
	}
	return string(b), nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
)

// jsonNodeKind identifies the JSON value type of a tree node.
type jsonNodeKind int8

const (
	kindObject jsonNodeKind = iota
	kindArray
	kindString
	kindNumber
	kindBool
	kindNull
)

// jsonNode is a node in a parsed JSON tree.
type jsonNode struct {
	kind      jsonNodeKind
	key       string // non-empty when this is an object field
	value     string // rendered value for primitive types
	children  []*jsonNode
	collapsed bool
	isLast    bool // last child in parent — no trailing comma
}

// visItem is an entry in the flattened visible-node list produced by flattenVisible.
type visItem struct {
	node           *jsonNode
	depth          int
	closingBracket bool // true → render the closing } or ] for this node's parent
}

// parseJSONTree parses a JSON string into a jsonNode tree.
// Returns nil if the input is not valid JSON or not an object/array at the root.
func parseJSONTree(s string) *jsonNode {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 {
		return nil
	}
	// Only attempt tree rendering for objects and arrays.
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}
	var raw any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil
	}
	return buildJSONNode(raw, "", true)
}

// buildJSONNode recursively converts an unmarshalled value into a jsonNode tree.
func buildJSONNode(v any, key string, isLast bool) *jsonNode {
	node := &jsonNode{key: key, isLast: isLast}
	switch val := v.(type) {
	case map[string]any:
		node.kind = kindObject
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for i, k := range keys {
			child := buildJSONNode(val[k], k, i == len(keys)-1)
			node.children = append(node.children, child)
		}
	case []any:
		node.kind = kindArray
		for i, item := range val {
			child := buildJSONNode(item, "", i == len(val)-1)
			node.children = append(node.children, child)
		}
	case string:
		node.kind = kindString
		node.value = fmt.Sprintf("%q", val)
	case float64:
		node.kind = kindNumber
		if val == float64(int64(val)) {
			node.value = fmt.Sprintf("%d", int64(val))
		} else {
			node.value = fmt.Sprintf("%g", val)
		}
	case bool:
		node.kind = kindBool
		if val {
			node.value = "true"
		} else {
			node.value = "false"
		}
	case nil:
		node.kind = kindNull
		node.value = "null"
	}
	return node
}

// flattenVisible returns a flat DFS-ordered list of all currently visible nodes.
// Closing-bracket entries are appended after each expanded object/array's children.
func flattenVisible(root *jsonNode) []visItem {
	var out []visItem
	appendVisible(root, 0, &out)
	return out
}

func appendVisible(node *jsonNode, depth int, out *[]visItem) {
	*out = append(*out, visItem{node: node, depth: depth})
	if node.collapsed || len(node.children) == 0 {
		return
	}
	for _, child := range node.children {
		appendVisible(child, depth+1, out)
	}
	// Append closing bracket line at the same depth as the opening line.
	*out = append(*out, visItem{node: node, depth: depth, closingBracket: true})
}

// toggleCollapse toggles the collapsed state of the node at the given cursor position.
// Both the opening line and the closing-bracket line of an object/array toggle it.
func toggleCollapse(vis []visItem, cursor int) {
	if cursor < 0 || cursor >= len(vis) {
		return
	}
	node := vis[cursor].node
	if node.kind == kindObject || node.kind == kindArray {
		node.collapsed = !node.collapsed
	}
}

// renderJSONTree renders a windowed view of the visible list, highlighting the cursor item.
// width is the available column width; visH is the number of lines to render.
func renderJSONTree(vis []visItem, cursor, scrollOff, width, visH int) string {
	if len(vis) == 0 {
		return ""
	}
	cursorBg := lipgloss.Color("#2a2e45")
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)

	var sb strings.Builder
	end := scrollOff + visH
	if end > len(vis) {
		end = len(vis)
	}
	for i := scrollOff; i < end; i++ {
		line := renderJSONItem(vis[i])
		if i == cursor {
			line = lipgloss.NewStyle().
				Background(cursorBg).
				Width(width - 2).
				Render(line)
		}
		sb.WriteString(line + "\n")
	}
	// Scroll position indicator when content overflows.
	if len(vis) > visH {
		pct := 0
		if len(vis) > 1 {
			pct = (cursor * 100) / (len(vis) - 1)
		}
		sb.WriteString(dimStyle.Render(fmt.Sprintf("  ─ %d/%d  %d%% ─", cursor+1, len(vis), pct)) + "\n")
	}
	return sb.String()
}

// nodeToAny reconstructs a Go value from a jsonNode tree (for re-serialization).
func nodeToAny(node *jsonNode) any {
	switch node.kind {
	case kindObject:
		m := make(map[string]any, len(node.children))
		for _, child := range node.children {
			m[child.key] = nodeToAny(child)
		}
		return m
	case kindArray:
		s := make([]any, len(node.children))
		for i, child := range node.children {
			s[i] = nodeToAny(child)
		}
		return s
	case kindString:
		var s string
		_ = json.Unmarshal([]byte(node.value), &s)
		return s
	case kindNumber:
		var n float64
		_ = json.Unmarshal([]byte(node.value), &n)
		return n
	case kindBool:
		return node.value == "true"
	case kindNull:
		return nil
	}
	return nil
}

// nodeToJSON serializes the selected node back to indented JSON.
func nodeToJSON(node *jsonNode) string {
	b, err := json.MarshalIndent(nodeToAny(node), "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// renderJSONItem converts a single visItem to a syntax-colored terminal line.
//
//nolint:gocyclo // switch on kind + collapsed/empty sub-cases; splitting would obscure the rendering logic
func renderJSONItem(item visItem) string {
	node := item.node
	indent := strings.Repeat("  ", item.depth)

	textStyle := lipgloss.NewStyle().Foreground(ui.ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ui.ColorDim)
	dim2Style := lipgloss.NewStyle().Foreground(ui.ColorDim2)
	keyStyle := lipgloss.NewStyle().Foreground(ui.ColorCyan)
	strStyle := lipgloss.NewStyle().Foreground(ui.ColorGreen)
	numStyle := lipgloss.NewStyle().Foreground(ui.ColorYellow)
	boolStyle := lipgloss.NewStyle().Foreground(ui.ColorPurple)

	comma := ""
	if !node.isLast {
		comma = textStyle.Render(",")
	}

	// Closing bracket line (}, ]).
	if item.closingBracket {
		bracket := func() string {
			if node.kind == kindObject {
				return "}"
			}
			return "]"
		}()
		return indent + textStyle.Render(bracket) + comma
	}

	// Key prefix for object fields.
	keyPart := ""
	if node.key != "" {
		keyPart = keyStyle.Render(fmt.Sprintf("%q", node.key)) + textStyle.Render(": ")
	}

	// Collapse/expand toggle indicator for objects and arrays.
	toggle := ""
	if node.kind == kindObject || node.kind == kindArray {
		if node.collapsed {
			toggle = dimStyle.Render("▶ ")
		} else {
			toggle = dimStyle.Render("▼ ")
		}
	}

	switch node.kind {
	case kindObject:
		if node.collapsed {
			return indent + toggle + keyPart + dim2Style.Render(fmt.Sprintf("{…%d}", len(node.children))) + comma
		}
		if len(node.children) == 0 {
			return indent + toggle + keyPart + textStyle.Render("{}") + comma
		}
		return indent + toggle + keyPart + textStyle.Render("{")
	case kindArray:
		if node.collapsed {
			return indent + toggle + keyPart + dim2Style.Render(fmt.Sprintf("[…%d]", len(node.children))) + comma
		}
		if len(node.children) == 0 {
			return indent + toggle + keyPart + textStyle.Render("[]") + comma
		}
		return indent + toggle + keyPart + textStyle.Render("[")
	case kindString:
		return indent + keyPart + strStyle.Render(node.value) + comma
	case kindNumber:
		return indent + keyPart + numStyle.Render(node.value) + comma
	case kindBool:
		return indent + keyPart + boolStyle.Render(node.value) + comma
	case kindNull:
		return indent + keyPart + dimStyle.Render(node.value) + comma
	}
	return indent + keyPart + node.value + comma
}

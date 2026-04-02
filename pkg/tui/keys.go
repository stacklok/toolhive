// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap holds all key bindings for the TUI.
type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	Tab         key.Binding
	ShiftTab    key.Binding
	Stop        key.Binding
	Restart     key.Binding
	Delete      key.Binding
	Filter      key.Binding
	Help        key.Binding
	Quit        key.Binding
	Enter       key.Binding
	Escape      key.Binding
	Follow      key.Binding
	Registry    key.Binding
	ScrollLeft  key.Binding
	ScrollRight key.Binding
	Space    key.Binding // toggle JSON node collapse
	CopyNode key.Binding // copy response JSON to clipboard (c)
	CopyCurl    key.Binding // copy curl command to clipboard (y)
	SearchNext  key.Binding // n — next search match in logs
	SearchPrev  key.Binding // N — previous search match in logs
	CopyURL     key.Binding // u — copy workload URL to clipboard
	ToolInfo    key.Binding // i — show tool description modal
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "navigate up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "navigate down"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch panel"),
	),
	ShiftTab: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "previous field"),
	),
	Stop: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "stop"),
	),
	Restart: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "restart"),
	),
	Delete: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "delete"),
	),
	Filter: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "confirm"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	),
	Follow: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "follow logs"),
	),
	Registry: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "registry"),
	),
	ScrollLeft: key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "scroll left"),
	),
	ScrollRight: key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "scroll right"),
	),
	Space: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "toggle collapse"),
	),
	CopyNode: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "copy node JSON"),
	),
	CopyCurl: key.NewBinding(
		key.WithKeys("y"),
		key.WithHelp("y", "copy curl"),
	),
	SearchNext: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "next match"),
	),
	SearchPrev: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "prev match"),
	),
	CopyURL: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "copy URL"),
	),
	ToolInfo: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "tool info"),
	),
}

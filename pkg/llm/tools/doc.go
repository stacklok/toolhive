// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tools provides the tool adapter registry and per-tool implementations
// for the thv llm setup/teardown commands.
//
// Each supported AI coding tool (Claude Code, Gemini CLI, Cursor, VS Code,
// Xcode) implements the Adapter interface. The Registry auto-discovers which
// tools are installed on the current machine and orchestrates Apply/Revert
// across all of them.
//
// Adding a new tool requires only:
//  1. Implementing the Adapter interface.
//  2. Calling Register() from an init() function in the new file.
//
// No changes to setup/teardown orchestration are needed.
package tools

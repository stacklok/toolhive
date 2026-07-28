// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"
)

var aiPluginCmd = &cobra.Command{
	Use:   "ai-plugin",
	Short: "Manage AI-tool plugins",
	Long: `The ai-plugin command provides subcommands to manage plugins for AI tools
(e.g. Claude Code, Codex) — not plugins for ToolHive itself.`,
}

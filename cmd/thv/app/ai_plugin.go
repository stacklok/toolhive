// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"
)

var aiPluginCmd = &cobra.Command{
	Use:   "ai-plugin",
	Short: "Manage AI-tool plugins",
	Long: `Manage plugins for AI tools such as Claude Code and Codex, not plugins
for ToolHive itself. A plugin is a manifest-based bundle that may contain
commands, agents, skills, hooks, and server declarations.`,
}

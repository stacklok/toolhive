// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	aiPluginUninstallScope       string
	aiPluginUninstallProjectRoot string
)

var aiPluginUninstallCmd = &cobra.Command{
	Use:               "uninstall [plugin-name]",
	Short:             "Uninstall an AI-tool plugin",
	Long:              `Remove a previously installed plugin by name.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAIPluginNames,
	PreRunE: chainPreRunE(
		validateAIPluginScope(&aiPluginUninstallScope),
		validateProjectRootForScope(&aiPluginUninstallScope, &aiPluginUninstallProjectRoot),
	),
	RunE: aiPluginUninstallCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginUninstallCmd)

	aiPluginUninstallCmd.Flags().StringVar(
		&aiPluginUninstallScope, "scope", string(plugins.ScopeUser), "Scope to uninstall from (user, project)",
	)
	aiPluginUninstallCmd.Flags().StringVar(
		&aiPluginUninstallProjectRoot, "project-root", "", "Project root path for project-scoped plugins",
	)
}

func aiPluginUninstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newAIPluginClient(cmd.Context())

	err := c.Uninstall(cmd.Context(), plugins.UninstallOptions{
		Name:        args[0],
		Scope:       plugins.Scope(aiPluginUninstallScope),
		ProjectRoot: aiPluginUninstallProjectRoot,
	})
	if err != nil {
		return formatAIPluginError("uninstall plugin", err)
	}

	return nil
}

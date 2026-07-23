// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	pluginUninstallScope       string
	pluginUninstallProjectRoot string
)

var pluginUninstallCmd = &cobra.Command{
	Use:               "uninstall [plugin-name]",
	Short:             "Uninstall a plugin",
	Long:              `Remove a previously installed plugin by name.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completePluginNames,
	PreRunE: chainPreRunE(
		validatePluginScope(&pluginUninstallScope),
		validateProjectRootForScope(&pluginUninstallScope, &pluginUninstallProjectRoot),
	),
	RunE: pluginUninstallCmdFunc,
}

func init() {
	pluginCmd.AddCommand(pluginUninstallCmd)

	pluginUninstallCmd.Flags().StringVar(
		&pluginUninstallScope, "scope", string(plugins.ScopeUser), "Scope to uninstall from (user, project)",
	)
	pluginUninstallCmd.Flags().StringVar(
		&pluginUninstallProjectRoot, "project-root", "", "Project root path for project-scoped plugins",
	)
}

func pluginUninstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newPluginClient(cmd.Context())

	err := c.Uninstall(cmd.Context(), plugins.UninstallOptions{
		Name:        args[0],
		Scope:       plugins.Scope(pluginUninstallScope),
		ProjectRoot: pluginUninstallProjectRoot,
	})
	if err != nil {
		return formatPluginError("uninstall plugin", err)
	}

	return nil
}

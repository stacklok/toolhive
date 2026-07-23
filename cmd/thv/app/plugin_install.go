// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	pluginInstallScope       string
	pluginInstallClientsRaw  string
	pluginInstallForce       bool
	pluginInstallProjectRoot string
	pluginInstallGroup       string
)

var pluginInstallCmd = &cobra.Command{
	Use:   "install [plugin-name]",
	Short: "Install a plugin",
	Long: `Install a plugin by name or OCI reference.
The plugin will be fetched from a remote registry and installed locally.`,
	Args: cobra.ExactArgs(1),
	PreRunE: chainPreRunE(
		validatePluginScope(&pluginInstallScope),
		validateProjectRootForScope(&pluginInstallScope, &pluginInstallProjectRoot),
		validateGroupFlag(),
	),
	RunE: pluginInstallCmdFunc,
}

func init() {
	pluginCmd.AddCommand(pluginInstallCmd)

	pluginInstallCmd.Flags().StringVar(&pluginInstallClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	pluginInstallCmd.Flags().StringVar(&pluginInstallScope, "scope", string(plugins.ScopeUser), "Installation scope (user, project)")
	pluginInstallCmd.Flags().BoolVar(&pluginInstallForce, "force", false, "Overwrite existing plugin directory")
	pluginInstallCmd.Flags().StringVar(
		&pluginInstallProjectRoot, "project-root", "", "Project root path for project-scoped installs",
	)
	pluginInstallCmd.Flags().StringVar(&pluginInstallGroup, "group", "", "Group to add the plugin to after installation")
}

func pluginInstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newPluginClient(cmd.Context())

	_, err := c.Install(cmd.Context(), plugins.InstallOptions{
		Name:        args[0],
		Scope:       plugins.Scope(pluginInstallScope),
		Clients:     parseSkillInstallClients(pluginInstallClientsRaw),
		Force:       pluginInstallForce,
		ProjectRoot: pluginInstallProjectRoot,
		Group:       pluginInstallGroup,
	})
	if err != nil {
		return formatPluginError("install plugin", err)
	}

	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	aiPluginInstallScope       string
	aiPluginInstallClientsRaw  string
	aiPluginInstallForce       bool
	aiPluginInstallProjectRoot string
	aiPluginInstallGroup       string
)

var aiPluginInstallCmd = &cobra.Command{
	Use:   "install [plugin-name]",
	Short: "Install a plugin",
	Long: `Install a plugin by name or OCI reference.
The plugin will be fetched from a remote registry and installed locally.`,
	Args: cobra.ExactArgs(1),
	PreRunE: chainPreRunE(
		validateAIPluginScope(&aiPluginInstallScope),
		validateProjectRootForScope(&aiPluginInstallScope, &aiPluginInstallProjectRoot),
		validateGroupFlag(),
	),
	RunE: aiPluginInstallCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginInstallCmd)

	aiPluginInstallCmd.Flags().StringVar(&aiPluginInstallClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	aiPluginInstallCmd.Flags().StringVar(
		&aiPluginInstallScope, "scope", string(plugins.ScopeUser), "Installation scope (user, project)",
	)
	aiPluginInstallCmd.Flags().BoolVar(&aiPluginInstallForce, "force", false, "Overwrite existing plugin directory")
	aiPluginInstallCmd.Flags().StringVar(
		&aiPluginInstallProjectRoot, "project-root", "", "Project root path for project-scoped installs",
	)
	aiPluginInstallCmd.Flags().StringVar(&aiPluginInstallGroup, "group", "", "Group to add the plugin to after installation")
}

func aiPluginInstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newAIPluginClient(cmd.Context())

	_, err := c.Install(cmd.Context(), plugins.InstallOptions{
		Name:        args[0],
		Scope:       plugins.Scope(aiPluginInstallScope),
		Clients:     parseSkillInstallClients(aiPluginInstallClientsRaw),
		Force:       aiPluginInstallForce,
		ProjectRoot: aiPluginInstallProjectRoot,
		Group:       aiPluginInstallGroup,
	})
	if err != nil {
		return formatAIPluginError("install plugin", err)
	}

	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	skillInstallScope       string
	skillInstallClient      string
	skillInstallForce       bool
	skillInstallProjectRoot string
	skillInstallGroup       string
)

var skillInstallCmd = &cobra.Command{
	Use:   "install [skill-name]",
	Short: "Install a skill",
	Long: `Install a skill by name or OCI reference.
The skill will be fetched from a remote registry and installed locally.`,
	Args: cobra.ExactArgs(1),
	PreRunE: chainPreRunE(
		validateSkillScope(&skillInstallScope),
		validateProjectRootForScope(&skillInstallScope, &skillInstallProjectRoot),
		validateGroupFlag(),
	),
	RunE: skillInstallCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillInstallCmd)

	skillInstallCmd.Flags().StringVar(&skillInstallClient, "client", "", "Target client application (e.g. claude-code)")
	skillInstallCmd.Flags().StringVar(&skillInstallScope, "scope", string(skills.ScopeUser), "Installation scope (user, project)")
	skillInstallCmd.Flags().BoolVar(&skillInstallForce, "force", false, "Overwrite existing skill directory")
	skillInstallCmd.Flags().StringVar(&skillInstallProjectRoot, "project-root", "", "Project root path for project-scoped installs")
	skillInstallCmd.Flags().StringVar(&skillInstallGroup, "group", "", "Group to add the skill to after installation")
}

func skillInstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient()

	_, err := c.Install(cmd.Context(), skills.InstallOptions{
		Name:        args[0],
		Scope:       skills.Scope(skillInstallScope),
		Client:      skillInstallClient,
		Force:       skillInstallForce,
		ProjectRoot: skillInstallProjectRoot,
		Group:       skillInstallGroup,
	})
	if err != nil {
		return formatSkillError("install skill", err)
	}

	return nil
}

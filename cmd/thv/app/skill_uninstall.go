// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	skillUninstallScope       string
	skillUninstallProjectRoot string
)

var skillUninstallCmd = &cobra.Command{
	Use:               "uninstall [skill-name]",
	Short:             "Uninstall a skill",
	Long:              `Remove a previously installed skill by name.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSkillNames,
	PreRunE: chainPreRunE(
		validateSkillScope(&skillUninstallScope),
		validateProjectRootForScope(&skillUninstallScope, &skillUninstallProjectRoot),
	),
	RunE: skillUninstallCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillUninstallCmd)

	skillUninstallCmd.Flags().StringVar(
		&skillUninstallScope, "scope", string(skills.ScopeUser), "Scope to uninstall from (user, project)",
	)
	skillUninstallCmd.Flags().StringVar(
		&skillUninstallProjectRoot, "project-root", "", "Project root path for project-scoped skills",
	)
}

func skillUninstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient()

	err := c.Uninstall(cmd.Context(), skills.UninstallOptions{
		Name:        args[0],
		Scope:       skills.Scope(skillUninstallScope),
		ProjectRoot: skillUninstallProjectRoot,
	})
	if err != nil {
		return formatSkillError("uninstall skill", err)
	}

	return nil
}

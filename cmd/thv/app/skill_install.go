// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	skillInstallScope       string
	skillInstallClientsRaw  string
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

	skillInstallCmd.Flags().StringVar(&skillInstallClientsRaw, "clients", "",
		`Comma-separated target client apps (e.g. claude-code,opencode), or "all" for every available client`)
	skillInstallCmd.Flags().StringVar(&skillInstallScope, "scope", string(skills.ScopeUser), "Installation scope (user, project)")
	skillInstallCmd.Flags().BoolVar(&skillInstallForce, "force", false, "Overwrite existing skill directory")
	skillInstallCmd.Flags().StringVar(&skillInstallProjectRoot, "project-root", "", "Project root path for project-scoped installs")
	skillInstallCmd.Flags().StringVar(&skillInstallGroup, "group", "", "Group to add the skill to after installation")
}

func skillInstallCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient(cmd.Context())

	_, err := c.Install(cmd.Context(), skills.InstallOptions{
		Name:        args[0],
		Scope:       skills.Scope(skillInstallScope),
		Clients:     parseSkillInstallClients(skillInstallClientsRaw),
		Force:       skillInstallForce,
		ProjectRoot: skillInstallProjectRoot,
		Group:       skillInstallGroup,
	})
	if err != nil {
		return formatSkillError("install skill", err)
	}

	return nil
}

// parseSkillInstallClients splits a comma-separated --clients flag value.
// Empty input yields nil so the server applies its default client.
func parseSkillInstallClients(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

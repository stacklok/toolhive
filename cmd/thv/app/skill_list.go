// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	skillListScope       string
	skillListClient      string
	skillListFormat      string
	skillListProjectRoot string
	skillListGroup       string
)

var skillListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List installed skills",
	Long:    `List all currently installed skills and their status.`,
	PreRunE: chainPreRunE(
		validateSkillScope(&skillListScope),
		ValidateFormat(&skillListFormat),
		validateGroupFlag(),
	),
	RunE: skillListCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillListCmd)

	skillListCmd.Flags().StringVar(&skillListScope, "scope", "", "Filter by scope (user, project)")
	skillListCmd.Flags().StringVar(&skillListClient, "client", "", "Filter by client application")
	AddFormatFlag(skillListCmd, &skillListFormat)
	AddGroupFlag(skillListCmd, &skillListGroup, false)
	skillListCmd.Flags().StringVar(&skillListProjectRoot, "project-root", "", "Project root path for project-scoped skills")
}

func skillListCmdFunc(cmd *cobra.Command, _ []string) error {
	c := newSkillClient()

	installed, err := c.List(cmd.Context(), skills.ListOptions{
		Scope:       skills.Scope(skillListScope),
		ClientApp:   skillListClient,
		ProjectRoot: skillListProjectRoot,
		Group:       skillListGroup,
	})
	if err != nil {
		return formatSkillError("list skills", err)
	}

	switch skillListFormat {
	case FormatJSON:
		if installed == nil {
			installed = []skills.InstalledSkill{}
		}
		data, err := json.MarshalIndent(installed, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		if len(installed) == 0 {
			if skillListScope != "" || skillListClient != "" {
				fmt.Println("No skills found matching filters")
			} else {
				fmt.Println("No skills installed")
			}
			return nil
		}
		printSkillListText(installed)
	}

	return nil
}

func printSkillListText(installed []skills.InstalledSkill) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSCOPE\tSTATUS\tCLIENTS\tREFERENCE")

	for _, s := range installed {
		clients := strings.Join(s.Clients, ", ")
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Metadata.Name,
			s.Metadata.Version,
			s.Scope,
			s.Status,
			clients,
			s.Reference,
		)
	}

	_ = w.Flush()
}

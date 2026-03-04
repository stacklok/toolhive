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
	skillInfoScope       string
	skillInfoFormat      string
	skillInfoProjectRoot string
)

var skillInfoCmd = &cobra.Command{
	Use:               "info [skill-name]",
	Short:             "Show skill details",
	Long:              `Display detailed information about a skill, including metadata, version, and installation status.`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeSkillNames,
	PreRunE: chainPreRunE(
		validateSkillScope(&skillInfoScope),
		ValidateFormat(&skillInfoFormat),
	),
	RunE: skillInfoCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillInfoCmd)

	skillInfoCmd.Flags().StringVar(&skillInfoScope, "scope", "", "Filter by scope (user, project)")
	AddFormatFlag(skillInfoCmd, &skillInfoFormat)
	skillInfoCmd.Flags().StringVar(&skillInfoProjectRoot, "project-root", "", "Project root path for project-scoped skills")
}

func skillInfoCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient()

	info, err := c.Info(cmd.Context(), skills.InfoOptions{
		Name:        args[0],
		Scope:       skills.Scope(skillInfoScope),
		ProjectRoot: skillInfoProjectRoot,
	})
	if err != nil {
		return formatSkillError("get skill info", err)
	}

	switch skillInfoFormat {
	case FormatJSON:
		data, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		printSkillInfoText(info)
	}

	return nil
}

func printSkillInfoText(info *skills.SkillInfo) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintf(w, "Name:\t%s\n", info.Metadata.Name)
	_, _ = fmt.Fprintf(w, "Version:\t%s\n", info.Metadata.Version)
	_, _ = fmt.Fprintf(w, "Description:\t%s\n", info.Metadata.Description)

	if s := info.InstalledSkill; s != nil {
		_, _ = fmt.Fprintf(w, "Scope:\t%s\n", s.Scope)
		_, _ = fmt.Fprintf(w, "Status:\t%s\n", s.Status)
		_, _ = fmt.Fprintf(w, "Reference:\t%s\n", s.Reference)
		_, _ = fmt.Fprintf(w, "Installed At:\t%s\n", s.InstalledAt.Format("2006-01-02 15:04:05"))
		if len(s.Clients) > 0 {
			_, _ = fmt.Fprintf(w, "Clients:\t%s\n", strings.Join(s.Clients, ", "))
		}
	}

	_ = w.Flush()
}

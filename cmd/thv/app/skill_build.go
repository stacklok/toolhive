// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var skillBuildTag string

var skillBuildCmd = &cobra.Command{
	Use:   "build [path]",
	Short: "Build a skill",
	Long: `Build a skill from a local directory into an OCI artifact that can be pushed to a registry.

On success, prints the OCI reference of the built artifact to stdout.`,
	Args: cobra.ExactArgs(1),
	ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	},
	RunE: skillBuildCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillBuildCmd)

	skillBuildCmd.Flags().StringVarP(&skillBuildTag, "tag", "t", "", "OCI tag for the built artifact")
}

func skillBuildCmdFunc(cmd *cobra.Command, args []string) error {
	absPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	c := newSkillClient()

	result, err := c.Build(cmd.Context(), skills.BuildOptions{
		Path: absPath,
		Tag:  skillBuildTag,
	})
	if err != nil {
		return formatSkillError("build skill", err)
	}

	fmt.Println(result.Reference)
	return nil
}

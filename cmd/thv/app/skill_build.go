// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

var skillBuildCmd = &cobra.Command{
	Use:   "build [path]",
	Short: "Build a skill",
	Long:  `Build a skill from a local directory into an OCI artifact that can be pushed to a registry.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, _ []string) error {
		return nil
	},
}

func init() {
	skillCmd.AddCommand(skillBuildCmd)
}

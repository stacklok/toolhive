// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

var skillInfoCmd = &cobra.Command{
	Use:   "info [skill-name]",
	Short: "Show skill details",
	Long:  `Display detailed information about a skill, including metadata, version, and installation status.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, _ []string) error {
		return nil
	},
}

func init() {
	skillCmd.AddCommand(skillInfoCmd)
}

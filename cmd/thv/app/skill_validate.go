// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

var skillValidateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate a skill definition",
	Long:  `Check that a skill definition in the given directory is valid and well-formed.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, _ []string) error {
		return nil
	},
}

func init() {
	skillCmd.AddCommand(skillValidateCmd)
}

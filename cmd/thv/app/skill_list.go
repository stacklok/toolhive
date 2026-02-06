// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

var skillListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed skills",
	Long:  `List all currently installed skills and their status.`,
	RunE: func(_ *cobra.Command, _ []string) error {
		return nil
	},
}

func init() {
	skillCmd.AddCommand(skillListCmd)
}

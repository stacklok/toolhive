// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

var skillUninstallCmd = &cobra.Command{
	Use:   "uninstall [skill-name]",
	Short: "Uninstall a skill",
	Long:  `Remove a previously installed skill by name.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, _ []string) error {
		return nil
	},
}

func init() {
	skillCmd.AddCommand(skillUninstallCmd)
}

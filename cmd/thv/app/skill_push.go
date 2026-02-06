// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "github.com/spf13/cobra"

var skillPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built skill",
	Long:  `Push a previously built skill artifact to a remote OCI registry.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, _ []string) error {
		return nil
	},
}

func init() {
	skillCmd.AddCommand(skillPushCmd)
}

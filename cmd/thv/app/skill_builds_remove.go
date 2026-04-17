// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"
)

var skillBuildsRemoveCmd = &cobra.Command{
	Use:   "remove <tag>",
	Short: "Remove a locally-built skill artifact",
	Long:  `Remove a locally-built OCI skill artifact and its blobs from the local OCI store.`,
	Args:  cobra.ExactArgs(1),
	RunE:  skillBuildsRemoveCmdFunc,
}

func init() {
	skillBuildsCmd.AddCommand(skillBuildsRemoveCmd)
}

func skillBuildsRemoveCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient(cmd.Context())
	if err := c.DeleteBuild(cmd.Context(), args[0]); err != nil {
		return formatSkillError("remove build", err)
	}
	fmt.Printf("Removed build %q\n", args[0])
	return nil
}

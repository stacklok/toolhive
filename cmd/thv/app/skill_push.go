// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var skillPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built skill",
	Long:  `Push a previously built skill artifact to a remote OCI registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  skillPushCmdFunc,
}

func init() {
	skillCmd.AddCommand(skillPushCmd)
}

func skillPushCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient()

	err := c.Push(cmd.Context(), skills.PushOptions{
		Reference: args[0],
	})
	if err != nil {
		return formatSkillError("push skill", err)
	}

	return nil
}

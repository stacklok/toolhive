// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/skills"
)

var (
	skillPushKey    string
	skillPushNoSign bool
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
	skillPushCmd.Flags().StringVar(&skillPushKey, "key", "", "Path to cosign private key for signing")
	skillPushCmd.Flags().BoolVar(&skillPushNoSign, "no-sign", false, "Skip post-push Sigstore signing")
}

func skillPushCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient(cmd.Context())

	err := c.Push(cmd.Context(), skills.PushOptions{
		Reference:   args[0],
		Key:         skillPushKey,
		SkipSigning: skillPushNoSign,
	})
	if err != nil {
		return formatSkillError("push skill", err)
	}

	return nil
}

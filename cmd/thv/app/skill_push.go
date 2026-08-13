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
	skillPushCmd.Flags().StringVar(&skillPushKey, "key", "",
		"Path to a cosign private key to sign the pushed artifact. "+
			"Encrypted keys are decrypted with COSIGN_PASSWORD read from the 'thv serve' process, "+
			"which performs the signing")
	skillPushCmd.Flags().BoolVar(&skillPushNoSign, "no-sign", false,
		"Push without signing (consumers will need an explicit unsigned exception to install project-scoped)")
}

func skillPushCmdFunc(cmd *cobra.Command, args []string) error {
	c := newSkillClient(cmd.Context())

	err := c.Push(cmd.Context(), skills.PushOptions{
		Reference: args[0],
		Key:       skillPushKey,
		NoSign:    skillPushNoSign,
	})
	if err != nil {
		return formatSkillError("push skill", err)
	}

	return nil
}

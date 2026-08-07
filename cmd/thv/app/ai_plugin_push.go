// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var aiPluginPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built AI-tool plugin to an OCI registry",
	Long:  `Push a previously built plugin artifact to a remote OCI registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  aiPluginPushCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginPushCmd)
}

func aiPluginPushCmdFunc(cmd *cobra.Command, args []string) error {
	c := newAIPluginClient(cmd.Context())

	err := c.Push(cmd.Context(), plugins.PushOptions{
		Reference: args[0],
	})
	if err != nil {
		return formatAIPluginError("push plugin", err)
	}

	return nil
}

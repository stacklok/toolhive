// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var pluginPushCmd = &cobra.Command{
	Use:   "push [reference]",
	Short: "Push a built plugin",
	Long:  `Push a previously built plugin artifact to a remote OCI registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  pluginPushCmdFunc,
}

func init() {
	pluginCmd.AddCommand(pluginPushCmd)
}

func pluginPushCmdFunc(cmd *cobra.Command, args []string) error {
	c := newPluginClient(cmd.Context())

	err := c.Push(cmd.Context(), plugins.PushOptions{
		Reference: args[0],
	})
	if err != nil {
		return formatPluginError("push plugin", err)
	}

	return nil
}

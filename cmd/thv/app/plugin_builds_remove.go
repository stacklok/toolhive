// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"
)

var pluginBuildsRemoveCmd = &cobra.Command{
	Use:   "remove <tag>",
	Short: "Remove a locally-built plugin artifact",
	Long:  `Remove a locally-built OCI plugin artifact and its blobs from the local OCI store.`,
	Args:  cobra.ExactArgs(1),
	RunE:  pluginBuildsRemoveCmdFunc,
}

func init() {
	pluginBuildsCmd.AddCommand(pluginBuildsRemoveCmd)
}

func pluginBuildsRemoveCmdFunc(cmd *cobra.Command, args []string) error {
	c := newPluginClient(cmd.Context())
	if err := c.DeleteBuild(cmd.Context(), args[0]); err != nil {
		return formatPluginError("remove build", err)
	}
	fmt.Printf("Removed build %q\n", args[0])
	return nil
}

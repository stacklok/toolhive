// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"
)

var aiPluginBuildsRemoveCmd = &cobra.Command{
	Use:   "remove <tag>",
	Short: "Remove a locally-built plugin artifact",
	Long:  `Remove a locally-built OCI plugin artifact and its blobs from the local OCI store.`,
	Args:  cobra.ExactArgs(1),
	RunE:  aiPluginBuildsRemoveCmdFunc,
}

func init() {
	aiPluginBuildsCmd.AddCommand(aiPluginBuildsRemoveCmd)
}

func aiPluginBuildsRemoveCmdFunc(cmd *cobra.Command, args []string) error {
	c := newAIPluginClient(cmd.Context())
	if err := c.DeleteBuild(cmd.Context(), args[0]); err != nil {
		return formatAIPluginError("remove build", err)
	}
	fmt.Printf("Removed build %q\n", args[0])
	return nil
}

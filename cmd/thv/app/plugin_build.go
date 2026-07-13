// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var pluginBuildTag string

var pluginBuildCmd = &cobra.Command{
	Use:   "build [path]",
	Short: "Build a plugin",
	Long: `Build a plugin from a local directory into an OCI artifact that can be pushed to a registry.

On success, prints the OCI reference of the built artifact to stdout.`,
	Args: cobra.ExactArgs(1),
	ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	},
	RunE: pluginBuildCmdFunc,
}

func init() {
	pluginCmd.AddCommand(pluginBuildCmd)

	pluginBuildCmd.Flags().StringVarP(&pluginBuildTag, "tag", "t", "", "OCI tag for the built artifact")
}

func pluginBuildCmdFunc(cmd *cobra.Command, args []string) error {
	absPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	c := newPluginClient(cmd.Context())

	result, err := c.Build(cmd.Context(), plugins.BuildOptions{
		Path: absPath,
		Tag:  pluginBuildTag,
	})
	if err != nil {
		return formatPluginError("build plugin", err)
	}

	fmt.Println(result.Reference)
	return nil
}

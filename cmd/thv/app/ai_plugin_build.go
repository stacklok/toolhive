// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var aiPluginBuildTag string

var aiPluginBuildCmd = &cobra.Command{
	Use:   "build [path]",
	Short: "Build an AI-tool plugin into a local OCI artifact",
	Long: `Build a plugin from a local directory into an OCI artifact that can be pushed to a registry.

On success, prints the OCI reference of the built artifact to stdout.`,
	Args: cobra.ExactArgs(1),
	ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	},
	RunE: aiPluginBuildCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginBuildCmd)

	aiPluginBuildCmd.Flags().StringVarP(&aiPluginBuildTag, "tag", "t", "", "OCI tag for the built artifact")
}

func aiPluginBuildCmdFunc(cmd *cobra.Command, args []string) error {
	absPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	c := newAIPluginClient(cmd.Context())

	result, err := c.Build(cmd.Context(), plugins.BuildOptions{
		Path: absPath,
		Tag:  aiPluginBuildTag,
	})
	if err != nil {
		return formatAIPluginError("build plugin", err)
	}

	fmt.Println(result.Reference)
	return nil
}

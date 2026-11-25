// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
)

var aiPluginValidateFormat string

var aiPluginValidateCmd = &cobra.Command{
	Use:   "validate [path]",
	Short: "Validate an AI-tool plugin directory",
	Long:  `Check that a plugin definition in the given directory is valid and well-formed.`,
	Args:  cobra.ExactArgs(1),
	ValidArgsFunction: func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	},
	PreRunE: ValidateFormat(&aiPluginValidateFormat),
	RunE:    aiPluginValidateCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginValidateCmd)

	AddFormatFlag(aiPluginValidateCmd, &aiPluginValidateFormat)
}

func aiPluginValidateCmdFunc(cmd *cobra.Command, args []string) error {
	absPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	c := newAIPluginClient(cmd.Context())

	result, err := c.Validate(cmd.Context(), absPath)
	if err != nil {
		return formatAIPluginError("validate plugin", err)
	}

	switch aiPluginValidateFormat {
	case FormatJSON:
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		for _, e := range result.Errors {
			fmt.Printf("Error: %s\n", e)
		}
		for _, w := range result.Warnings {
			fmt.Printf("Warning: %s\n", w)
		}
	}

	if !result.Valid {
		return fmt.Errorf("plugin validation failed")
	}

	return nil
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/plugins"
)

var (
	aiPluginListScope       string
	aiPluginListClient      string
	aiPluginListFormat      string
	aiPluginListProjectRoot string
	aiPluginListGroup       string
)

var aiPluginListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List installed plugins",
	Long:    `List all currently installed plugins and their status.`,
	PreRunE: chainPreRunE(
		validateAIPluginScope(&aiPluginListScope),
		ValidateFormat(&aiPluginListFormat),
		validateGroupFlag(),
	),
	RunE: aiPluginListCmdFunc,
}

func init() {
	aiPluginCmd.AddCommand(aiPluginListCmd)

	aiPluginListCmd.Flags().StringVar(&aiPluginListScope, "scope", "", "Filter by scope (user, project)")
	aiPluginListCmd.Flags().StringVar(&aiPluginListClient, "client", "", "Filter by client application")
	AddFormatFlag(aiPluginListCmd, &aiPluginListFormat)
	AddGroupFlag(aiPluginListCmd, &aiPluginListGroup, false)
	aiPluginListCmd.Flags().StringVar(&aiPluginListProjectRoot, "project-root", "", "Project root path for project-scoped plugins")
}

func aiPluginListCmdFunc(cmd *cobra.Command, _ []string) error {
	c := newAIPluginClient(cmd.Context())

	installed, err := c.List(cmd.Context(), plugins.ListOptions{
		Scope:       plugins.Scope(aiPluginListScope),
		ClientApp:   aiPluginListClient,
		ProjectRoot: aiPluginListProjectRoot,
		Group:       aiPluginListGroup,
	})
	if err != nil {
		return formatAIPluginError("list plugins", err)
	}

	switch aiPluginListFormat {
	case FormatJSON:
		if installed == nil {
			installed = []plugins.InstalledPlugin{}
		}
		data, err := json.MarshalIndent(installed, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(data))
	default:
		if len(installed) == 0 {
			if aiPluginListScope != "" || aiPluginListClient != "" {
				fmt.Println("No plugins found matching filters")
			} else {
				fmt.Println("No plugins installed")
			}
			return nil
		}
		printAIPluginListText(installed)
	}

	return nil
}

func printAIPluginListText(installed []plugins.InstalledPlugin) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tVERSION\tSCOPE\tSTATUS\tCLIENTS\tREFERENCE")

	for _, p := range installed {
		clients := strings.Join(p.Clients, ", ")
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.Metadata.Name,
			p.Metadata.Version,
			p.Scope,
			p.Status,
			clients,
			p.Reference,
		)
	}

	_ = w.Flush()
}

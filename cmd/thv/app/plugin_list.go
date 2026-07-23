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
	pluginListScope       string
	pluginListClient      string
	pluginListFormat      string
	pluginListProjectRoot string
	pluginListGroup       string
)

var pluginListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List installed plugins",
	Long:    `List all currently installed plugins and their status.`,
	PreRunE: chainPreRunE(
		validatePluginScope(&pluginListScope),
		ValidateFormat(&pluginListFormat),
		validateGroupFlag(),
	),
	RunE: pluginListCmdFunc,
}

func init() {
	pluginCmd.AddCommand(pluginListCmd)

	pluginListCmd.Flags().StringVar(&pluginListScope, "scope", "", "Filter by scope (user, project)")
	pluginListCmd.Flags().StringVar(&pluginListClient, "client", "", "Filter by client application")
	AddFormatFlag(pluginListCmd, &pluginListFormat)
	AddGroupFlag(pluginListCmd, &pluginListGroup, false)
	pluginListCmd.Flags().StringVar(&pluginListProjectRoot, "project-root", "", "Project root path for project-scoped plugins")
}

func pluginListCmdFunc(cmd *cobra.Command, _ []string) error {
	c := newPluginClient(cmd.Context())

	installed, err := c.List(cmd.Context(), plugins.ListOptions{
		Scope:       plugins.Scope(pluginListScope),
		ClientApp:   pluginListClient,
		ProjectRoot: pluginListProjectRoot,
		Group:       pluginListGroup,
	})
	if err != nil {
		return formatPluginError("list plugins", err)
	}

	switch pluginListFormat {
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
			if pluginListScope != "" || pluginListClient != "" {
				fmt.Println("No plugins found matching filters")
			} else {
				fmt.Println("No plugins installed")
			}
			return nil
		}
		printPluginListText(installed)
	}

	return nil
}

func printPluginListText(installed []plugins.InstalledPlugin) {
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

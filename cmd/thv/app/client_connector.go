// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/client"
)

var (
	connectorClient string
	connectorFormat string
)

var clientConnectorCmd = &cobra.Command{
	Use:   "connector",
	Short: "Manage remote MCP servers configured directly in a client",
	Long: `Manage remote MCP servers ("connectors") that a client connects to directly,
with no local ToolHive proxy. The client signs in to each server itself (OAuth).

Only Claude Desktop in third-party mode is supported. Run
"thv llm setup --client claude-desktop" first: connectors are written into the
same configuration, and Claude Desktop applies one configuration at a time.

Claude Desktop reads its configuration only at launch, so fully quit and reopen
it after adding or removing a connector. A new connector then shows a Connect
button under Customize → Connectors.

Re-running "thv llm setup --client claude-desktop" keeps existing connectors.
"thv llm teardown" removes them.`,
}

var clientConnectorAddCmd = &cobra.Command{
	Use:   "add NAME URL",
	Short: "Add a remote MCP server to a client",
	Long: `Add a remote MCP server to a client, or update the URL of an existing
connector with the same name. The URL must be an absolute https URL, and it
must be the server's final address: Claude Desktop does not follow HTTP
redirects, even one that only adds a trailing slash.

A warning is logged when Claude Desktop will not load the connector: when a
managed-preferences (MDM) profile overrides its local configuration, or when a
different saved configuration is applied in the app.

Example:
  thv client connector add --client claude-desktop stacklok-connectors https://connector-gateway.example.com/gw/mcp`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		cm, err := client.NewClientManager()
		if err != nil {
			return fmt.Errorf("initializing client manager: %w", err)
		}
		return cm.AddConnector(client.ClientApp(connectorClient), args[0], args[1])
	},
}

var clientConnectorRmCmd = &cobra.Command{
	Use:   "rm NAME",
	Short: "Remove a remote MCP server from a client",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		cm, err := client.NewClientManager()
		if err != nil {
			return fmt.Errorf("initializing client manager: %w", err)
		}
		return cm.RemoveConnector(client.ClientApp(connectorClient), args[0])
	},
	ValidArgsFunction: completeConnectorNames,
}

var clientConnectorListCmd = &cobra.Command{
	Use:   "list",
	Short: "List remote MCP servers configured in a client",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		cm, err := client.NewClientManager()
		if err != nil {
			return fmt.Errorf("initializing client manager: %w", err)
		}
		connectors, err := cm.ListConnectors(client.ClientApp(connectorClient))
		if err != nil {
			return err
		}
		return printConnectors(connectors, connectorFormat)
	},
}

func init() {
	clientCmd.AddCommand(clientConnectorCmd)
	clientConnectorCmd.AddCommand(clientConnectorAddCmd, clientConnectorRmCmd, clientConnectorListCmd)

	clientConnectorCmd.PersistentFlags().StringVar(&connectorClient, "client", "",
		fmt.Sprintf("Client to configure (supported: %s)", client.ClaudeDesktop))
	_ = clientConnectorCmd.MarkPersistentFlagRequired("client") // flag is defined above
	_ = clientConnectorCmd.RegisterFlagCompletionFunc("client",
		func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return []string{string(client.ClaudeDesktop)}, cobra.ShellCompDirectiveNoFileComp
		}) // flag is defined above

	AddFormatFlag(clientConnectorListCmd, &connectorFormat)
	clientConnectorListCmd.PreRunE = ValidateFormat(&connectorFormat)
}

func completeConnectorNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 || connectorClient == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cm, err := client.NewClientManager()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	connectors, err := cm.ListConnectors(client.ClientApp(connectorClient))
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(connectors))
	for _, c := range connectors {
		names = append(names, c.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func printConnectors(connectors []client.Connector, format string) error {
	if format == FormatJSON {
		data, err := json.MarshalIndent(connectors, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding connectors: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}
	if len(connectors) == 0 {
		fmt.Println("No connectors configured")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tURL")
	for _, c := range connectors {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", c.Name, c.URL)
	}
	return w.Flush()
}

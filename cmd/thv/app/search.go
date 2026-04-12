// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry"
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for MCP servers",
	Long: `Search for MCP servers in the registry by name, description, or tags.
This is a convenience alias for 'thv registry search'.`,
	Args: cobra.ExactArgs(1),
	RunE: searchCmdFunc,
}

var (
	searchFormat       string
	searchRegistryName string
)

func init() {
	// Add search command to root command (top-level convenience alias)
	rootCmd.AddCommand(searchCmd)

	// Add flags for search command
	searchCmd.Flags().StringVar(&searchFormat, "format", FormatText, "Output format (json or text)")
	searchCmd.Flags().StringVar(&searchRegistryName, "registry", "", "Registry name to search (default: configured default)")
}

func searchCmdFunc(_ *cobra.Command, args []string) error {
	query := args[0]
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	serverJSONs, err := store.SearchServers(searchRegistryName, query)
	if err != nil {
		return fmt.Errorf("failed to search servers: %w", err)
	}

	servers, err := registry.ConvertServersToServerMetadata(serverJSONs)
	if err != nil {
		return fmt.Errorf("failed to convert servers: %w", err)
	}

	if len(servers) == 0 {
		fmt.Printf("No servers found matching query: %s\n", query)
		return nil
	}

	types.SortServersByName(servers)

	switch searchFormat {
	case FormatJSON:
		jsonData, err := json.MarshalIndent(servers, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	default:
		fmt.Printf("Found %d servers matching query: %s\n", len(servers), query)
		printTextSearchResults(servers)
	}

	return nil
}

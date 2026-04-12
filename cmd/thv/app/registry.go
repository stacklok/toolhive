// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
	transtypes "github.com/stacklok/toolhive/pkg/transport/types"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage MCP server registries",
	Long:  `Manage MCP server registries, including listing registries, listing servers, getting server details, and searching.`,
}

var registryListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List configured registries",
	Long:    `List all configured registry sources.`,
	RunE:    registryListCmdFunc,
}

var registryServersCmd = &cobra.Command{
	Use:     "servers",
	Aliases: []string{"srv"},
	Short:   "List available MCP servers",
	Long:    `List all available MCP servers in the registry.`,
	RunE:    registryServersCmdFunc,
}

var registryServerCmd = &cobra.Command{
	Use:   "server [name]",
	Short: "Get information about an MCP server",
	Long:  `Get detailed information about a specific MCP server in the registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  registryServerCmdFunc,
}

var registrySkillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "List available skills",
	Long:  `List all available skills in the registry.`,
	RunE:  registrySkillsCmdFunc,
}

var registrySearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for MCP servers",
	Long:  `Search for MCP servers in the registry by name, description, or tags.`,
	Args:  cobra.ExactArgs(1),
	RunE:  registrySearchCmdFunc,
}

var registrySetDefaultCmd = &cobra.Command{
	Use:   "set-default [name]",
	Short: "Set the default registry",
	Long:  `Set the default registry used for server lookups.`,
	Args:  cobra.ExactArgs(1),
	RunE:  registrySetDefaultCmdFunc,
}

var (
	registryFormat   string
	refreshRegistry  bool
	registryNameFlag string
)

func init() {
	// Add registry command to root command
	rootCmd.AddCommand(registryCmd)

	// Add subcommands to registry command
	registryCmd.AddCommand(registryListCmd)
	registryCmd.AddCommand(registryServersCmd)
	registryCmd.AddCommand(registryServerCmd)
	registryCmd.AddCommand(registrySkillsCmd)
	registryCmd.AddCommand(registrySearchCmd)
	registryCmd.AddCommand(registrySetDefaultCmd)

	// Add flags for servers command
	AddFormatFlag(registryServersCmd, &registryFormat)
	registryServersCmd.Flags().BoolVar(&refreshRegistry, "refresh", false, "Force refresh registry cache")
	registryServersCmd.Flags().StringVar(&registryNameFlag, "registry", "", "Registry name to query (default: configured default)")
	registryServersCmd.PreRunE = ValidateFormat(&registryFormat)

	// Add flags for server command
	AddFormatFlag(registryServerCmd, &registryFormat)
	registryServerCmd.Flags().BoolVar(&refreshRegistry, "refresh", false, "Force refresh registry cache")
	registryServerCmd.Flags().StringVar(&registryNameFlag, "registry", "", "Registry name to query (default: configured default)")
	registryServerCmd.PreRunE = ValidateFormat(&registryFormat)

	// Add flags for skills command
	AddFormatFlag(registrySkillsCmd, &registryFormat)
	registrySkillsCmd.Flags().StringVar(&registryNameFlag, "registry", "", "Registry name to query (default: configured default)")
	registrySkillsCmd.PreRunE = ValidateFormat(&registryFormat)

	// Add flags for search command
	AddFormatFlag(registrySearchCmd, &registryFormat)
	registrySearchCmd.Flags().StringVar(&registryNameFlag, "registry", "", "Registry name to search (default: configured default)")
	registrySearchCmd.PreRunE = ValidateFormat(&registryFormat)

	// Add flags for list command
	AddFormatFlag(registryListCmd, &registryFormat)
	registryListCmd.PreRunE = ValidateFormat(&registryFormat)
}

func registryListCmdFunc(_ *cobra.Command, _ []string) error {
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	names := store.ListRegistries()

	switch registryFormat {
	case FormatJSON:
		type registryEntry struct {
			Name      string `json:"name"`
			IsDefault bool   `json:"is_default"`
			Proxied   bool   `json:"proxied"`
		}
		entries := make([]registryEntry, 0, len(names))
		defaultName := store.DefaultRegistryName()
		for _, name := range names {
			entries = append(entries, registryEntry{
				Name:      name,
				IsDefault: name == defaultName,
				Proxied:   store.IsProxied(name),
			})
		}
		jsonData, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	default:
		defaultName := store.DefaultRegistryName()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAME\tTYPE\tDEFAULT"); err != nil {
			slog.Warn(fmt.Sprintf("Failed to write output: %v", err))
			return nil
		}
		for _, name := range names {
			regType := "local"
			if store.IsProxied(name) {
				regType = "proxied"
			}
			isDefault := ""
			if name == defaultName {
				isDefault = "*"
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", name, regType, isDefault); err != nil {
				slog.Debug(fmt.Sprintf("Failed to write registry info: %v", err))
			}
		}
		if err := w.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to flush tabwriter: %v\n", err)
		}
	}

	return nil
}

func registryServersCmdFunc(_ *cobra.Command, _ []string) error {
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	serverJSONs, err := store.ListServers(registryNameFlag)
	if err != nil {
		return fmt.Errorf("failed to list servers: %w", err)
	}

	servers, err := registry.ConvertServersToServerMetadata(serverJSONs)
	if err != nil {
		return fmt.Errorf("failed to convert servers: %w", err)
	}

	types.SortServersByName(servers)

	switch registryFormat {
	case FormatJSON:
		return printJSONServers(servers)
	default:
		printTextServers(servers)
		return nil
	}
}

func registryServerCmdFunc(_ *cobra.Command, args []string) error {
	serverName := args[0]
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	serverJSON, err := store.GetServer(registryNameFlag, serverName)
	if err != nil {
		return fmt.Errorf("failed to get server information: %w", err)
	}

	server, err := registry.ConvertServerJSONToMetadata(serverJSON)
	if err != nil {
		return fmt.Errorf("failed to convert server: %w", err)
	}

	switch registryFormat {
	case FormatJSON:
		return printJSONServer(server)
	default:
		printTextServerInfo(serverName, server)
		return nil
	}
}

func registrySkillsCmdFunc(_ *cobra.Command, _ []string) error {
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	skills, err := store.ListSkills(registryNameFlag)
	if err != nil {
		return fmt.Errorf("failed to list skills: %w", err)
	}

	switch registryFormat {
	case FormatJSON:
		jsonData, err := json.MarshalIndent(skills, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonData))
	default:
		if len(skills) == 0 {
			fmt.Println("No skills found.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		if _, err := fmt.Fprintln(w, "NAMESPACE\tNAME\tDESCRIPTION"); err != nil {
			slog.Warn(fmt.Sprintf("Failed to write output: %v", err))
			return nil
		}
		for _, sk := range skills {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n",
				sk.Namespace,
				sk.Name,
				truncateString(sk.Description, 50),
			); err != nil {
				slog.Debug(fmt.Sprintf("Failed to write skill info: %v", err))
			}
		}
		if err := w.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to flush tabwriter: %v\n", err)
		}
	}

	return nil
}

func registrySearchCmdFunc(_ *cobra.Command, args []string) error {
	query := args[0]
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	serverJSONs, err := store.SearchServers(registryNameFlag, query)
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

	switch registryFormat {
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

func registrySetDefaultCmdFunc(_ *cobra.Command, args []string) error {
	name := args[0]

	provider := registry.NewConfigurator()

	// Verify the registry exists
	store, err := registry.DefaultStore()
	if err != nil {
		return fmt.Errorf("failed to get registry store: %w", err)
	}

	names := store.ListRegistries()
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("registry %q not found; available registries: %s", name, strings.Join(names, ", "))
	}

	_ = provider // The SetDefaultRegistry is on config.Provider, not Configurator
	// Use the config provider directly
	configProvider := config.NewDefaultProvider()
	if err := configProvider.SetDefaultRegistry(name); err != nil {
		return fmt.Errorf("failed to set default registry: %w", err)
	}

	registry.ResetDefaultStore()

	return nil
}

// printJSONServers prints servers in JSON format
func printJSONServers(servers []types.ServerMetadata) error {
	jsonData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	fmt.Println(string(jsonData))
	return nil
}

// printJSONServer prints a single server in JSON format
func printJSONServer(server types.ServerMetadata) error {
	jsonData, err := json.MarshalIndent(server, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	fmt.Println(string(jsonData))
	return nil
}

// printTextServers prints servers in text format
func printTextServers(servers []types.ServerMetadata) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tTYPE\tDESCRIPTION\tTIER\tSTARS\tPULLS"); err != nil {
		slog.Warn(fmt.Sprintf("Failed to write output: %v", err))
		return
	}

	for _, server := range servers {
		stars := 0
		if metadata := server.GetMetadata(); metadata != nil {
			stars = metadata.Stars
		}

		desc := server.GetDescription()
		if server.GetStatus() == "Deprecated" {
			desc = "**DEPRECATED** " + desc
		}

		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
			server.GetName(),
			getServerType(server),
			truncateString(desc, 50),
			server.GetTier(),
			stars,
		); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write server information: %v", err))
		}
	}

	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to flush tabwriter: %v\n", err)
	}
}

// ServerType constants
const (
	ServerTypeRemote    = "remote"
	ServerTypeContainer = "container"
)

// getServerType returns the type of server (container or remote)
func getServerType(server types.ServerMetadata) string {
	if server.IsRemote() {
		return ServerTypeRemote
	}
	return ServerTypeContainer
}

// printTextServerInfo prints detailed information about a server in text format
// nolint:gocyclo
func printTextServerInfo(name string, server types.ServerMetadata) {
	fmt.Printf("Name: %s\n", server.GetName())
	fmt.Printf("Type: %s\n", getServerType(server))
	fmt.Printf("Description: %s\n", server.GetDescription())
	fmt.Printf("Tier: %s\n", server.GetTier())
	fmt.Printf("Status: %s\n", server.GetStatus())
	fmt.Printf("Transport: %s\n", server.GetTransport())

	// Type-specific information
	if !server.IsRemote() {
		// Container server
		if img, ok := server.(*types.ImageMetadata); ok {
			fmt.Printf("Image: %s\n", img.Image)
			isHTTPTransport := img.Transport == transtypes.TransportTypeSSE.String() ||
				img.Transport == transtypes.TransportTypeStreamableHTTP.String()
			if isHTTPTransport && img.TargetPort > 0 {
				fmt.Printf("Target Port: %d\n", img.TargetPort)
			}
			fmt.Printf("Has Provenance: %s\n", map[bool]string{true: "Yes", false: "No"}[img.Provenance != nil])

			// Print permissions
			if img.Permissions != nil {
				fmt.Println("\nPermissions:")

				if len(img.Permissions.Read) > 0 {
					fmt.Println("  Read:")
					for _, path := range img.Permissions.Read {
						fmt.Printf("    - %s\n", path)
					}
				}

				if len(img.Permissions.Write) > 0 {
					fmt.Println("  Write:")
					for _, path := range img.Permissions.Write {
						fmt.Printf("    - %s\n", path)
					}
				}

				if img.Permissions.Network != nil && img.Permissions.Network.Outbound != nil {
					fmt.Println("  Network:")
					outbound := img.Permissions.Network.Outbound

					if outbound.InsecureAllowAll {
						fmt.Println("    Insecure Allow All: true")
					}

					if len(outbound.AllowHost) > 0 {
						fmt.Printf("    Allow Host: %s\n", strings.Join(outbound.AllowHost, ", "))
					}

					if len(outbound.AllowPort) > 0 {
						ports := make([]string, len(outbound.AllowPort))
						for i, port := range outbound.AllowPort {
							ports[i] = fmt.Sprintf("%d", port)
						}
						fmt.Printf("    Allow Port: %s\n", strings.Join(ports, ", "))
					}
				}
			}
		}
	} else {
		// Remote server
		if remote, ok := server.(*types.RemoteServerMetadata); ok {
			fmt.Printf("URL: %s\n", remote.URL)

			if len(remote.Headers) > 0 {
				fmt.Println("\nHeaders:")
				for _, header := range remote.Headers {
					required := ""
					if header.Required {
						required = " (required)"
					}
					defaultValue := ""
					if header.Default != "" {
						defaultValue = fmt.Sprintf(" [default: %s]", header.Default)
					}
					fmt.Printf("  - %s%s%s: %s\n", header.Name, required, defaultValue, header.Description)
				}
			}

			if remote.OAuthConfig != nil {
				fmt.Println("\nOAuth Configuration:")
				if remote.OAuthConfig.Issuer != "" {
					fmt.Printf("  Issuer: %s\n", remote.OAuthConfig.Issuer)
				}
				if remote.OAuthConfig.ClientID != "" {
					fmt.Printf("  Client ID: %s\n", remote.OAuthConfig.ClientID)
				}
				if len(remote.OAuthConfig.Scopes) > 0 {
					fmt.Printf("  Scopes: %s\n", strings.Join(remote.OAuthConfig.Scopes, ", "))
				}
			}
		}
	}

	fmt.Printf("Repository URL: %s\n", server.GetRepositoryURL())

	if metadata := server.GetMetadata(); metadata != nil {
		fmt.Printf("Popularity: %d stars\n", metadata.Stars)
		fmt.Printf("Last Updated: %s\n", metadata.LastUpdated)
	} else {
		fmt.Printf("Popularity: 0 stars\n")
		fmt.Printf("Last Updated: N/A\n")
	}

	if tools := server.GetTools(); len(tools) > 0 {
		fmt.Println("\nTools:")
		for _, tool := range tools {
			fmt.Printf("  - %s\n", tool)
		}
	}

	if envVars := server.GetEnvVars(); len(envVars) > 0 {
		fmt.Println("\nEnvironment Variables:")
		for _, envVar := range envVars {
			required := ""
			if envVar.Required {
				required = " (required)"
			}
			defaultValue := ""
			if envVar.Default != "" {
				defaultValue = fmt.Sprintf(" [default: %s]", envVar.Default)
			}
			fmt.Printf("  - %s%s%s: %s\n", envVar.Name, required, defaultValue, envVar.Description)
		}
	}

	if tags := server.GetTags(); len(tags) > 0 {
		fmt.Println("\nTags:")
		fmt.Printf("  %s\n", strings.Join(tags, ", "))
	}

	if customMetadata := server.GetCustomMetadata(); len(customMetadata) > 0 {
		fmt.Println("\nCustom Metadata:")
		for key, value := range customMetadata {
			fmt.Printf("  %s: %v\n", key, value)
		}
	}

	fmt.Println("\nExample Command:")
	fmt.Printf("  thv run %s\n", name)
}

// truncateString truncates a string to the specified length and adds "..." if truncated
// It also sanitizes the string by replacing newlines and multiple spaces with single spaces
func truncateString(s string, maxLen int) string {
	// Replace newlines and tabs with spaces
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")

	// Replace multiple consecutive spaces with a single space
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}

	// Trim leading/trailing spaces
	s = strings.TrimSpace(s)

	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// printTextSearchResults prints servers in text format for search results
func printTextSearchResults(servers []types.ServerMetadata) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tTYPE\tDESCRIPTION\tTRANSPORT\tSTARS\tPULLS"); err != nil {
		slog.Warn(fmt.Sprintf("Failed to write output: %v", err))
		return
	}

	for _, server := range servers {
		stars := 0
		if metadata := server.GetMetadata(); metadata != nil {
			stars = metadata.Stars
		}

		serverType := "container"
		if server.IsRemote() {
			serverType = "remote"
		}

		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
			server.GetName(),
			serverType,
			truncateString(server.GetDescription(), 50),
			server.GetTransport(),
			stars,
		); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write server information: %v", err))
		}
	}

	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to flush tabwriter: %v\n", err)
	}
}

// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
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

	"github.com/stacklok/toolhive/pkg/registry"
	types "github.com/stacklok/toolhive/pkg/registry/registry"
	transtypes "github.com/stacklok/toolhive/pkg/transport/types"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage MCP server registry",
	Long:  `Manage the MCP server registry, including listing and getting information about available MCP servers.`,
}

var registryListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List available MCP servers",
	Long:    `List all available MCP servers in the registry.`,
	RunE:    registryListCmdFunc,
}

var registryInfoCmd = &cobra.Command{
	Use:   "info [server]",
	Short: "Get information about an MCP server",
	Long:  `Get detailed information about a specific MCP server in the registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  registryInfoCmdFunc,
}

var (
	registryFormat  string
	refreshRegistry bool
)

func init() {
	// Add registry command to root command
	rootCmd.AddCommand(registryCmd)

	// Add subcommands to registry command
	registryCmd.AddCommand(registryListCmd)
	registryCmd.AddCommand(registryInfoCmd)

	// Add flags for list and info commands
	AddFormatFlag(registryListCmd, &registryFormat)
	registryListCmd.Flags().BoolVar(&refreshRegistry, "refresh", false, "Force refresh registry cache")
	registryListCmd.PreRunE = ValidateFormat(&registryFormat)

	AddFormatFlag(registryInfoCmd, &registryFormat)
	registryInfoCmd.Flags().BoolVar(&refreshRegistry, "refresh", false, "Force refresh registry cache")
	registryInfoCmd.PreRunE = ValidateFormat(&registryFormat)
}

func registryListCmdFunc(_ *cobra.Command, _ []string) error {
	// Get all servers from registry
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return fmt.Errorf("failed to get registry provider: %w", err)
	}

	// Force refresh if requested
	if refreshRegistry {
		if cached, ok := provider.(*registry.CachedAPIRegistryProvider); ok {
			if err := cached.ForceRefresh(); err != nil {
				return fmt.Errorf("failed to refresh registry: %w", err)
			}
		}
	}

	servers, err := provider.ListServers()
	if err != nil {
		return fmt.Errorf("failed to list servers: %w", err)
	}

	// Sort servers by name using the utility function
	types.SortServersByName(servers)

	// Output based on format
	switch registryFormat {
	case FormatJSON:
		return printJSONServers(servers)
	default:
		printTextServers(servers)
		return nil
	}
}

func registryInfoCmdFunc(_ *cobra.Command, args []string) error {
	// Get server information
	serverName := args[0]
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return fmt.Errorf("failed to get registry provider: %w", err)
	}

	// Force refresh if requested
	if refreshRegistry {
		if cached, ok := provider.(*registry.CachedAPIRegistryProvider); ok {
			if err := cached.ForceRefresh(); err != nil {
				return fmt.Errorf("failed to refresh registry: %w", err)
			}
		}
	}

	server, err := provider.GetServer(serverName)
	if err != nil {
		return fmt.Errorf("failed to get server information: %w", err)
	}

	// Output based on format
	switch registryFormat {
	case FormatJSON:
		return printJSONServer(server)
	default:
		printTextServerInfo(serverName, server)
		return nil
	}
}

// printJSONServers prints servers in JSON format
func printJSONServers(servers []types.ServerMetadata) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Print JSON
	fmt.Println(string(jsonData))
	return nil
}

// printJSONServer prints a single server in JSON format
func printJSONServer(server types.ServerMetadata) error {
	jsonData, err := json.MarshalIndent(server, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Print JSON
	fmt.Println(string(jsonData))
	return nil
}

// printTextServers prints servers in text format
func printTextServers(servers []types.ServerMetadata) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tTYPE\tDESCRIPTION\tTIER\tSTARS\tPULLS"); err != nil {
		slog.Warn(fmt.Sprintf("Failed to write output: %v", err))
		return
	}

	// Print server information
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

	// Flush the tabwriter
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

				// Print read permissions
				if len(img.Permissions.Read) > 0 {
					fmt.Println("  Read:")
					for _, path := range img.Permissions.Read {
						fmt.Printf("    - %s\n", path)
					}
				}

				// Print write permissions
				if len(img.Permissions.Write) > 0 {
					fmt.Println("  Write:")
					for _, path := range img.Permissions.Write {
						fmt.Printf("    - %s\n", path)
					}
				}

				// Print network permissions
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

			// Print headers
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

			// Print OAuth config
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

	// Print metadata
	if metadata := server.GetMetadata(); metadata != nil {
		fmt.Printf("Popularity: %d stars\n", metadata.Stars)
		fmt.Printf("Last Updated: %s\n", metadata.LastUpdated)
	} else {
		fmt.Printf("Popularity: 0 stars\n")
		fmt.Printf("Last Updated: N/A\n")
	}

	// Print tools
	if tools := server.GetTools(); len(tools) > 0 {
		fmt.Println("\nTools:")
		for _, tool := range tools {
			fmt.Printf("  - %s\n", tool)
		}
	}

	// Print environment variables
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

	// Print tags
	if tags := server.GetTags(); len(tags) > 0 {
		fmt.Println("\nTags:")
		fmt.Printf("  %s\n", strings.Join(tags, ", "))
	}

	// Print custom metadata
	if customMetadata := server.GetCustomMetadata(); len(customMetadata) > 0 {
		fmt.Println("\nCustom Metadata:")
		for key, value := range customMetadata {
			fmt.Printf("  %s: %v\n", key, value)
		}
	}

	// Print example command
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

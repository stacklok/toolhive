// Package main provides the entry point for the toolhive command-line application.
// This file contains the implementation of the 'registry' command.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
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
	registryFormat string
)

func init() {
	// Add registry command to root command
	rootCmd.AddCommand(registryCmd)

	// Add subcommands to registry command
	registryCmd.AddCommand(registryListCmd)
	registryCmd.AddCommand(registryInfoCmd)

	// Add flags for list and info commands
	registryListCmd.Flags().StringVar(&registryFormat, "format", "text", "Output format (json or text)")
	registryInfoCmd.Flags().StringVar(&registryFormat, "format", "text", "Output format (json or text)")
}

func registryListCmdFunc(_ *cobra.Command, _ []string) error {
	// Get all servers from registry
	servers, err := registry.ListServers()
	if err != nil {
		return fmt.Errorf("failed to list servers: %v", err)
	}

	// Sort servers by name
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Image < servers[j].Image
	})

	// Output based on format
	switch registryFormat {
	case "json":
		return printJSONServers(servers)
	default:
		printTextServers(servers)
		return nil
	}
}

func registryInfoCmdFunc(_ *cobra.Command, args []string) error {
	// Get server information
	serverName := args[0]
	server, err := registry.GetServer(serverName)
	if err != nil {
		return fmt.Errorf("failed to get server information: %v", err)
	}

	// Output based on format
	switch registryFormat {
	case "json":
		return printJSONServer(server)
	default:
		printTextServerInfo(serverName, server)
		return nil
	}
}

// printJSONServers prints servers in JSON format
func printJSONServers(servers []*registry.Server) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON
	logger.Log.Info(string(jsonData))
	return nil
}

// printJSONServer prints a single server in JSON format
func printJSONServer(server *registry.Server) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(server, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON
	logger.Log.Info(string(jsonData))
	return nil
}

// printTextServers prints servers in text format
func printTextServers(servers []*registry.Server) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION\tTRANSPORT\tSTARS\tPULLS")

	// Print server information
	for _, server := range servers {
		// Extract server name from image
		name := strings.Split(server.Image, ":")[0]
		name = strings.TrimPrefix(name, "mcp/")

		// Print server information
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
			name,
			truncateString(server.Description, 60),
			server.Transport,
			server.Metadata.Stars,
			server.Metadata.Pulls,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Log.Warn(fmt.Sprintf("Warning: Failed to flush tabwriter: %v", err))
	}
}

// printTextServerInfo prints detailed information about a server in text format
// nolint:gocyclo
func printTextServerInfo(name string, server *registry.Server) {
	logger.Log.Info(fmt.Sprintf("Name: %s", name))
	logger.Log.Info(fmt.Sprintf("Image: %s", server.Image))
	logger.Log.Info(fmt.Sprintf("Description: %s", server.Description))
	logger.Log.Info(fmt.Sprintf("Transport: %s", server.Transport))
	logger.Log.Info(fmt.Sprintf("Repository URL: %s", server.RepositoryURL))
	logger.Log.Info(fmt.Sprintf("Popularity: %d stars, %d pulls", server.Metadata.Stars, server.Metadata.Pulls))
	logger.Log.Info(fmt.Sprintf("Last Updated: %s", server.Metadata.LastUpdated))

	// Print tools
	if len(server.Tools) > 0 {
		logger.Log.Info("Tools:")
		for _, tool := range server.Tools {
			logger.Log.Info(fmt.Sprintf("  - %s", tool))
		}
	}

	// Print environment variables
	if len(server.EnvVars) > 0 {
		logger.Log.Info("\nEnvironment Variables:")
		for _, envVar := range server.EnvVars {
			required := ""
			if envVar.Required {
				required = " (required)"
			}
			defaultValue := ""
			if envVar.Default != "" {
				defaultValue = fmt.Sprintf(" [default: %s]", envVar.Default)
			}
			logger.Log.Info(fmt.Sprintf("  - %s%s%s: %s", envVar.Name, required, defaultValue, envVar.Description))
		}
	}

	// Print tags
	if len(server.Tags) > 0 {
		logger.Log.Info("Tags:")
		logger.Log.Info(fmt.Sprintf("  %s", strings.Join(server.Tags, ", ")))
	}

	// Print permissions
	if server.Permissions != nil {
		logger.Log.Info("Permissions:")

		// Print read permissions
		if len(server.Permissions.Read) > 0 {
			logger.Log.Info("  Read:")
			for _, path := range server.Permissions.Read {
				logger.Log.Info(fmt.Sprintf("    - %s", path))
			}
		}

		// Print write permissions
		if len(server.Permissions.Write) > 0 {
			logger.Log.Info("  Write:")
			for _, path := range server.Permissions.Write {
				logger.Log.Info(fmt.Sprintf("    - %s", path))
			}
		}

		// Print network permissions
		if server.Permissions.Network != nil && server.Permissions.Network.Outbound != nil {
			logger.Log.Info("  Network:")
			outbound := server.Permissions.Network.Outbound

			if outbound.InsecureAllowAll {
				logger.Log.Info("    Insecure Allow All: true")
			}

			if len(outbound.AllowTransport) > 0 {
				logger.Log.Info(fmt.Sprintf("    Allow Transport: %s", strings.Join(outbound.AllowTransport, ", ")))
			}

			if len(outbound.AllowHost) > 0 {
				logger.Log.Info(fmt.Sprintf("    Allow Host: %s", strings.Join(outbound.AllowHost, ", ")))
			}

			if len(outbound.AllowPort) > 0 {
				ports := make([]string, len(outbound.AllowPort))
				for i, port := range outbound.AllowPort {
					ports[i] = fmt.Sprintf("%d", port)
				}
				logger.Log.Info(fmt.Sprintf("    Allow Port: %s", strings.Join(ports, ", ")))
			}
		}
	}

	// Print example command
	logger.Log.Info("Example Command:")
	logger.Log.Info(fmt.Sprintf("  thv run %s", name))
}

// truncateString truncates a string to the specified length and adds "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

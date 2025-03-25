// Package main provides the entry point for the vibetool command-line application.
// This file contains the implementation of the 'registry' command.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/registry"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage MCP server registry",
	Long:  `Manage the MCP server registry, including listing, searching, and getting information about available MCP servers.`,
}

var registryListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available MCP servers",
	Long:  `List all available MCP servers in the registry.`,
	RunE:  registryListCmdFunc,
}

var registrySearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for MCP servers",
	Long:  `Search for MCP servers in the registry by name, description, or tags.`,
	Args:  cobra.ExactArgs(1),
	RunE:  registrySearchCmdFunc,
}

var registryInfoCmd = &cobra.Command{
	Use:   "info [server]",
	Short: "Get information about an MCP server",
	Long:  `Get detailed information about a specific MCP server in the registry.`,
	Args:  cobra.ExactArgs(1),
	RunE:  registryInfoCmdFunc,
}

var registryRunCmd = &cobra.Command{
	Use:   "run [server] [-- ARGS...]",
	Short: "Run an MCP server using registry defaults",
	Long: `Run an MCP server using defaults from the registry.
This command simplifies running MCP servers by automatically using the correct transport,
permissions, and other settings from the registry. You can still override any setting
with flags.`,
	Args: cobra.MinimumNArgs(1),
	RunE: registryRunCmdFunc,
}

var (
	registryFormat string

	// Flags for registry run command
	registryRunTransport         string
	registryRunPort              int
	registryRunTargetPort        int
	registryRunPermissionProfile string
	registryRunEnv               []string
	registryRunNoClientConfig    bool
	registryRunForeground        bool
)

func init() {
	// Add registry command to root command
	rootCmd.AddCommand(registryCmd)

	// Add subcommands to registry command
	registryCmd.AddCommand(registryListCmd)
	registryCmd.AddCommand(registrySearchCmd)
	registryCmd.AddCommand(registryInfoCmd)
	registryCmd.AddCommand(registryRunCmd)

	// Add flags for list, search, and info commands
	registryListCmd.Flags().StringVar(&registryFormat, "format", "text", "Output format (json or text)")
	registrySearchCmd.Flags().StringVar(&registryFormat, "format", "text", "Output format (json or text)")
	registryInfoCmd.Flags().StringVar(&registryFormat, "format", "text", "Output format (json or text)")

	// Add flags for run command (same as the main run command)
	registryRunCmd.Flags().StringVar(
		&registryRunTransport,
		"transport",
		"",
		"Transport mode (sse or stdio) - defaults to registry value",
	)
	registryRunCmd.Flags().IntVar(&registryRunPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	registryRunCmd.Flags().IntVar(
		&registryRunTargetPort,
		"target-port",
		0,
		"Port for the container to expose (only applicable to SSE transport)",
	)
	registryRunCmd.Flags().StringVar(
		&registryRunPermissionProfile,
		"permission-profile",
		"",
		"Permission profile to use (stdio, network, or path to JSON file) - defaults to registry value",
	)
	registryRunCmd.Flags().StringArrayVarP(
		&registryRunEnv,
		"env",
		"e",
		[]string{},
		"Environment variables to pass to the MCP server (format: KEY=VALUE)",
	)
	registryRunCmd.Flags().BoolVar(
		&registryRunNoClientConfig,
		"no-client-config",
		false,
		"Do not update client configuration files with the MCP server URL",
	)
	registryRunCmd.Flags().BoolVarP(
		&registryRunForeground,
		"foreground",
		"f",
		false,
		"Run in foreground mode (block until container exits)",
	)

	// Add OIDC validation flags
	AddOIDCFlags(registryRunCmd)
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

func registrySearchCmdFunc(_ *cobra.Command, args []string) error {
	// Search for servers
	query := args[0]
	servers, err := registry.SearchServers(query)
	if err != nil {
		return fmt.Errorf("failed to search servers: %v", err)
	}

	if len(servers) == 0 {
		fmt.Printf("No servers found matching query: %s\n", query)
		return nil
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
		fmt.Printf("Found %d servers matching query: %s\n", len(servers), query)
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
	fmt.Println(string(jsonData))
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
	fmt.Println(string(jsonData))
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
		fmt.Printf("Warning: Failed to flush tabwriter: %v\n", err)
	}
}

// printTextServerInfo prints detailed information about a server in text format
// nolint:gocyclo
func printTextServerInfo(name string, server *registry.Server) {
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Image: %s\n", server.Image)
	fmt.Printf("Description: %s\n", server.Description)
	fmt.Printf("Transport: %s\n", server.Transport)
	fmt.Printf("Repository URL: %s\n", server.RepositoryURL)
	fmt.Printf("Popularity: %d stars, %d pulls\n", server.Metadata.Stars, server.Metadata.Pulls)
	fmt.Printf("Last Updated: %s\n", server.Metadata.LastUpdated)

	// Print tools
	if len(server.Tools) > 0 {
		fmt.Println("\nTools:")
		for _, tool := range server.Tools {
			fmt.Printf("  - %s\n", tool)
		}
	}

	// Print environment variables
	if len(server.EnvVars) > 0 {
		fmt.Println("\nEnvironment Variables:")
		for _, envVar := range server.EnvVars {
			required := ""
			if envVar.Required {
				required = " (required)"
			}
			fmt.Printf("  - %s%s: %s\n", envVar.Name, required, envVar.Description)
		}
	}

	// Print tags
	if len(server.Tags) > 0 {
		fmt.Println("\nTags:")
		fmt.Printf("  %s\n", strings.Join(server.Tags, ", "))
	}

	// Print permissions
	if server.Permissions != nil {
		fmt.Println("\nPermissions:")

		// Print read permissions
		if len(server.Permissions.Read) > 0 {
			fmt.Println("  Read:")
			for _, path := range server.Permissions.Read {
				fmt.Printf("    - %s\n", path)
			}
		}

		// Print write permissions
		if len(server.Permissions.Write) > 0 {
			fmt.Println("  Write:")
			for _, path := range server.Permissions.Write {
				fmt.Printf("    - %s\n", path)
			}
		}

		// Print network permissions
		if server.Permissions.Network != nil && server.Permissions.Network.Outbound != nil {
			fmt.Println("  Network:")
			outbound := server.Permissions.Network.Outbound

			if outbound.InsecureAllowAll {
				fmt.Println("    Insecure Allow All: true")
			}

			if len(outbound.AllowTransport) > 0 {
				fmt.Printf("    Allow Transport: %s\n", strings.Join(outbound.AllowTransport, ", "))
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

	// Print example command
	fmt.Println("\nExample Command:")
	fmt.Printf("  vt run --transport %s --name %s %s\n", server.Transport, name, server.Image)
}

// truncateString truncates a string to the specified length and adds "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// registryRunCmdFunc handles the registry run command
func registryRunCmdFunc(cmd *cobra.Command, args []string) error {
	// Get the server name from arguments
	serverName := args[0]

	// Get additional command arguments (after --)
	cmdArgs := []string{}
	if len(args) > 1 {
		cmdArgs = args[1:]
	}

	// Get server information from registry
	server, err := registry.GetServer(serverName)
	if err != nil {
		return fmt.Errorf("failed to get server information: %v", err)
	}

	// Extract image name from server
	image := server.Image

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Process environment variables
	// First add required environment variables from registry
	envVars := make([]string, 0, len(registryRunEnv)+len(server.EnvVars))

	// Copy existing env vars
	envVars = append(envVars, registryRunEnv...)

	// Add required environment variables from registry if not already provided
	for _, envVar := range server.EnvVars {
		if envVar.Required {
			// Check if the environment variable is already provided in the command line
			found := false
			for _, env := range envVars {
				if strings.HasPrefix(env, envVar.Name+"=") {
					found = true
					break
				}
			}

			if !found {
				// Ask the user for the required environment variable
				fmt.Printf("Required environment variable: %s (%s)\n", envVar.Name, envVar.Description)
				fmt.Printf("Enter value for %s: ", envVar.Name)
				var value string
				if _, err := fmt.Scanln(&value); err != nil {
					fmt.Printf("Warning: Failed to read input: %v\n", err)
				}

				if value != "" {
					envVars = append(envVars, fmt.Sprintf("%s=%s", envVar.Name, value))
				}
			}
		}
	}

	// Get OIDC flag values
	oidcIssuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
	oidcAudience := GetStringFlagOrEmpty(cmd, "oidc-audience")
	oidcJwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	oidcClientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

	// Determine transport
	transport := server.Transport
	if registryRunTransport != "" {
		transport = registryRunTransport
	}

	// Create a temporary file for the permission profile
	tempFile, err := os.CreateTemp("", fmt.Sprintf("vibetool-%s-permissions-*.json", serverName))
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %v", err)
	}
	defer tempFile.Close()

	// Get the temporary file path
	permProfilePath := tempFile.Name()

	// Serialize the permission profile to JSON
	permProfileJSON, err := json.Marshal(server.Permissions)
	if err != nil {
		return fmt.Errorf("failed to serialize permission profile: %v", err)
	}

	// Write the permission profile to the temporary file
	if _, err := tempFile.Write(permProfileJSON); err != nil {
		return fmt.Errorf("failed to write permission profile to file: %v", err)
	}

	// Only print debug message if debug mode is enabled
	if debugMode {
		fmt.Printf("Wrote permission profile to temporary file: %s\n", permProfilePath)
	}

	// Create run options
	options := RunOptions{
		Image:             image,
		CmdArgs:           cmdArgs,
		Transport:         transport,
		Name:              serverName,
		Port:              registryRunPort,
		TargetPort:        registryRunTargetPort,
		PermissionProfile: permProfilePath,
		EnvVars:           envVars,
		NoClientConfig:    registryRunNoClientConfig,
		Foreground:        registryRunForeground,
		OIDCIssuer:        oidcIssuer,
		OIDCAudience:      oidcAudience,
		OIDCJwksURL:       oidcJwksURL,
		OIDCClientID:      oidcClientID,
		Debug:             debugMode,
	}

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the MCP server
	return RunMCPServer(ctx, cmd, options)
}

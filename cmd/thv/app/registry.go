package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

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
	registryListCmd.Flags().StringVar(&registryFormat, "format", FormatText, "Output format (json or text)")
	registryInfoCmd.Flags().StringVar(&registryFormat, "format", FormatText, "Output format (json or text)")
}

func registryListCmdFunc(_ *cobra.Command, _ []string) error {
	// Get all servers from registry
	servers, err := registry.ListServers()
	if err != nil {
		return fmt.Errorf("failed to list servers: %v", err)
	}

	// Sort servers by name
	sort.Slice(servers, func(i, j int) bool {
		return servers[i].Name < servers[j].Name
	})

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
	server, err := registry.GetServer(serverName)
	if err != nil {
		return fmt.Errorf("failed to get server information: %v", err)
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
		stars := 0
		pulls := 0
		if server.Metadata != nil {
			stars = server.Metadata.Stars
			pulls = server.Metadata.Pulls
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
			server.Name,
			truncateString(server.Description, 60),
			server.Transport,
			stars,
			pulls,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to flush tabwriter: %v\n", err)
	}
}

// printTextServerInfo prints detailed information about a server in text format
// nolint:gocyclo
func printTextServerInfo(name string, server *registry.Server) {
	fmt.Printf("Name: %s\n", server.Name)
	fmt.Printf("Image: %s\n", server.Image)
	fmt.Printf("Description: %s\n", server.Description)
	fmt.Printf("Transport: %s\n", server.Transport)
	if server.Transport == "sse" && server.TargetPort > 0 {
		fmt.Printf("Target Port: %d\n", server.TargetPort)
	}
	fmt.Printf("Repository URL: %s\n", server.RepositoryURL)

	if server.Metadata != nil {
		fmt.Printf("Popularity: %d stars, %d pulls\n", server.Metadata.Stars, server.Metadata.Pulls)
		fmt.Printf("Last Updated: %s\n", server.Metadata.LastUpdated)
	} else {
		fmt.Printf("Popularity: 0 stars, 0 pulls\n")
		fmt.Printf("Last Updated: N/A\n")
	}

	// Print tools
	if len(server.Tools) > 0 {
		fmt.Println("Tools:")
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
			defaultValue := ""
			if envVar.Default != "" {
				defaultValue = fmt.Sprintf(" [default: %s]", envVar.Default)
			}
			fmt.Printf("  - %s%s%s: %s\n", envVar.Name, required, defaultValue, envVar.Description)
		}
	}

	// Print tags
	if len(server.Tags) > 0 {
		fmt.Println("Tags:")
		fmt.Printf("  %s\n", strings.Join(server.Tags, ", "))
	}

	// Print permissions
	if server.Permissions != nil {
		fmt.Println("Permissions:")

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
	fmt.Println("Example Command:")
	fmt.Printf("  thv run %s\n", name)
}

// truncateString truncates a string to the specified length and adds "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

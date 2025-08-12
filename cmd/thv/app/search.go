package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/registry"
)

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search for MCP servers",
	Long:  `Search for MCP servers in the registry by name, description, or tags.`,
	Args:  cobra.ExactArgs(1),
	RunE:  searchCmdFunc,
}

var (
	searchFormat string
)

func init() {
	// Add search command to root command
	rootCmd.AddCommand(searchCmd)

	// Add flags for search command
	searchCmd.Flags().StringVar(&searchFormat, "format", FormatText, "Output format (json or text)")
}

func searchCmdFunc(_ *cobra.Command, args []string) error {
	// Search for servers
	query := args[0]
	provider, err := registry.GetDefaultProvider()
	if err != nil {
		return fmt.Errorf("failed to get registry provider: %v", err)
	}
	servers, err := provider.SearchServers(query)
	if err != nil {
		return fmt.Errorf("failed to search servers: %v", err)
	}

	if len(servers) == 0 {
		fmt.Printf("No servers found matching query: %s\n", query)
		return nil
	}

	// Sort servers by name using the utility function
	registry.SortServersByName(servers)

	// Output based on format
	switch searchFormat {
	case FormatJSON:
		return printJSONSearchResults(servers)
	default:
		fmt.Printf("Found %d servers matching query: %s\n", len(servers), query)
		printTextSearchResults(servers)
		return nil
	}
}

// printJSONSearchResults prints servers in JSON format
func printJSONSearchResults(servers []registry.ServerMetadata) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON
	fmt.Println(string(jsonData))
	return nil
}

// printTextSearchResults prints servers in text format
func printTextSearchResults(servers []registry.ServerMetadata) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tDESCRIPTION\tTRANSPORT\tSTARS\tPULLS")

	// Print server information
	for _, server := range servers {
		stars := 0
		pulls := 0
		if metadata := server.GetMetadata(); metadata != nil {
			stars = metadata.Stars
			pulls = metadata.Pulls
		}

		serverType := "container"
		if server.IsRemote() {
			serverType = "remote"
		}

		// Print server information
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
			server.GetName(),
			serverType,
			truncateSearchString(server.GetDescription(), 50),
			server.GetTransport(),
			stars,
			pulls,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to flush tabwriter: %v\n", err)
	}
}

// truncateSearchString truncates a string to the specified length and adds "..." if truncated
func truncateSearchString(s string, maxLen int) string {
	return truncateString(s, maxLen)
}

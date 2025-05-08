package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
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
		return servers[i].Name < servers[j].Name
	})

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
func printJSONSearchResults(servers []*registry.Server) error {
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
func printTextSearchResults(servers []*registry.Server) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION\tTRANSPORT\tSTARS\tPULLS")

	// Print server information
	for _, server := range servers {
		// Print server information
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
			server.Name,
			truncateSearchString(server.Description, 60),
			server.Transport,
			server.Metadata.Stars,
			server.Metadata.Pulls,
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

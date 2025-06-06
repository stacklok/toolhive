package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List running MCP servers",
	Long:  `List all MCP servers managed by ToolHive, including their status and configuration.`,
	RunE:  listCmdFunc,
}

var (
	listAll    bool
	listFormat string
)

func init() {
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all containers (default shows just running)")
	listCmd.Flags().StringVar(&listFormat, "format", FormatText, "Output format (json, text, or mcpservers)")
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Instantiate the container manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	// Create container runtime
	toolHiveContainers, err := manager.ListWorkloads(ctx, listAll)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	if len(toolHiveContainers) == 0 {
		fmt.Println("No MCP servers found")
		return nil
	}

	// Output based on format
	switch listFormat {
	case FormatJSON:
		return printJSONOutput(toolHiveContainers)
	case "mcpservers":
		return printMCPServersOutput(toolHiveContainers)
	default:
		printTextOutput(toolHiveContainers)
		return nil
	}
}

// printJSONOutput prints container information in JSON format
func printJSONOutput(containers []workloads.Workload) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(containers, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON directly to stdout
	fmt.Println(string(jsonData))
	return nil
}

// printMCPServersOutput prints MCP servers configuration in JSON format
// This format is compatible with client configuration files
func printMCPServersOutput(containers []workloads.Workload) error {
	// Create a map to hold the MCP servers configuration
	mcpServers := make(map[string]map[string]string)

	for _, c := range containers {
		// Add the MCP server to the map
		mcpServers[c.Name] = map[string]string{
			"url": c.URL,
		}
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(map[string]interface{}{
		"mcpServers": mcpServers,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON directly to stdout
	fmt.Println(string(jsonData))
	return nil
}

// printTextOutput prints container information in text format
func printTextOutput(containers []workloads.Workload) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tPACKAGE\tSTATUS\tURL\tPORT\tTOOL TYPE\tCREATED AT")

	// Print container information
	for _, c := range containers {
		// Print container information
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			c.Name,
			c.Package,
			c.Status,
			c.URL,
			c.Port,
			c.ToolType,
			c.CreatedAt,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Errorf("Warning: Failed to flush tabwriter: %v", err)
	}
}

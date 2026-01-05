package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List running MCP servers",
	Long: `List all MCP servers managed by ToolHive, including their status and configuration.

Examples:
  # List running MCP servers
  thv list

  # List all MCP servers (including stopped)
  thv list --all

  # List servers in JSON format
  thv list --format json

  # List servers in a specific group
  thv list --group production

  # List servers with specific labels
  thv list --label env=dev --label team=backend`,
	RunE: listCmdFunc,
}

var (
	listAll         bool
	listFormat      string
	listLabelFilter []string
	listGroupFilter string
)

func init() {
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all workloads (default shows just running)")
	listCmd.Flags().StringVar(&listFormat, "format", FormatText, "Output format (json, text, or mcpservers)")
	listCmd.Flags().StringArrayVarP(&listLabelFilter, "label", "l", []string{}, "Filter workloads by labels (format: key=value)")
	listCmd.Flags().StringVar(&listGroupFilter, "group", "", "Filter workloads by group")

	listCmd.PreRunE = validateGroupFlag()
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Instantiate the status manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create status manager: %w", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, listAll, listLabelFilter...)
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}

	// Apply group filtering if specified
	if listGroupFilter != "" {
		workloadList, err = workloads.FilterByGroup(workloadList, listGroupFilter)
		if err != nil {
			return fmt.Errorf("failed to filter workloads by group: %w", err)
		}
	}

	// Output based on format
	switch listFormat {
	case FormatJSON:
		return printJSONOutput(workloadList)
	case "mcpservers":
		return printMCPServersOutput(workloadList)
	default:
		// For text format, handle empty list with a message
		if len(workloadList) == 0 {
			if listGroupFilter != "" {
				fmt.Printf("No MCP servers found in group '%s'\n", listGroupFilter)
			} else {
				fmt.Println("No MCP servers found")
			}
			return nil
		}
		printTextOutput(workloadList)
		return nil
	}
}

// printJSONOutput prints workload information in JSON format
func printJSONOutput(workloadList []core.Workload) error {
	// Ensure we have a non-nil slice to avoid null in JSON output
	if workloadList == nil {
		workloadList = []core.Workload{}
	}

	// Sort workloads alphabetically by name for deterministic output
	core.SortWorkloadsByName(workloadList)

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(workloadList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Print JSON directly to stdout
	fmt.Println(string(jsonData))
	return nil
}

// printMCPServersOutput prints MCP servers configuration in JSON format
// This format is compatible with client configuration files
func printMCPServersOutput(workloadList []core.Workload) error {
	// Create a map to hold the MCP servers configuration
	mcpServers := make(map[string]map[string]string)

	for _, c := range workloadList {
		// Add the MCP server to the map
		mcpServers[c.Name] = map[string]string{
			"url":  c.URL,
			"type": c.ProxyMode,
		}
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(map[string]interface{}{
		"mcpServers": mcpServers,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// Print JSON directly to stdout
	fmt.Println(string(jsonData))
	return nil
}

// printTextOutput prints workload information in text format
func printTextOutput(workloadList []core.Workload) {
	// Sort workloads alphabetically by name for deterministic output
	core.SortWorkloadsByName(workloadList)

	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(w, "NAME\tPACKAGE\tSTATUS\tURL\tPORT\tGROUP\tCREATED"); err != nil {
		logger.Warnf("Failed to write output header: %v", err)
		return
	}

	// Print workload information
	for _, c := range workloadList {
		// Highlight unauthenticated workloads with a warning indicator
		status := string(c.Status)
		if c.Status == rt.WorkloadStatusUnauthenticated {
			status = "⚠️  " + status
		}

		// Print workload information
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			c.Name,
			c.Package,
			status,
			c.URL,
			c.Port,
			c.Group,
			c.CreatedAt,
		); err != nil {
			logger.Debugf("Failed to write workload information: %v", err)
		}
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Errorf("Warning: Failed to flush tabwriter: %v", err)
	}
}

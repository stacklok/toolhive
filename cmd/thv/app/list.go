package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
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
	listAll         bool
	listFormat      string
	listLabelFilter []string
	listGroupFilter string
)

func init() {
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all workloads (default shows just running)")
	listCmd.Flags().StringVar(&listFormat, "format", FormatText, "Output format (json, text, or mcpservers)")
	listCmd.Flags().StringArrayVarP(&listLabelFilter, "label", "l", []string{}, "Filter workloads by labels (format: key=value)")
	// TODO: Re-enable when group functionality is complete
	// listCmd.Flags().StringVar(&listGroupFilter, "group", "", "Filter workloads by group")
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Instantiate the status manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create status manager: %v", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, listAll, listLabelFilter...)
	if err != nil {
		return fmt.Errorf("failed to list workloads: %v", err)
	}

	// Apply group filtering if specified
	if listGroupFilter != "" {
		workloadList, err = filterWorkloadsByGroup(ctx, workloadList, listGroupFilter)
		if err != nil {
			return fmt.Errorf("failed to filter workloads by group: %v", err)
		}
	}

	if len(workloadList) == 0 {
		if listGroupFilter != "" {
			fmt.Printf("No MCP servers found in group '%s'\n", listGroupFilter)
		} else {
			fmt.Println("No MCP servers found")
		}
		return nil
	}

	// Output based on format
	switch listFormat {
	case FormatJSON:
		return printJSONOutput(workloadList)
	case "mcpservers":
		return printMCPServersOutput(workloadList)
	default:
		printTextOutput(workloadList)
		return nil
	}
}

// filterWorkloadsByGroup filters workloads to only include those in the specified group
func filterWorkloadsByGroup(
	ctx context.Context, workloadList []workloads.Workload, groupName string,
) ([]workloads.Workload, error) {
	// Create group manager
	groupManager, err := groups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create group manager: %v", err)
	}

	// Check if the group exists
	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %v", err)
	}
	if !exists {
		return nil, fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Get all workload names in the specified group
	groupWorkloadNames, err := groupManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %v", err)
	}

	groupWorkloadMap := make(map[string]struct{})
	for _, name := range groupWorkloadNames {
		groupWorkloadMap[name] = struct{}{}
	}

	// Filter workloads that belong to the specified group
	var filteredWorkloads []workloads.Workload
	for _, workload := range workloadList {
		if _, ok := groupWorkloadMap[workload.Name]; ok {
			filteredWorkloads = append(filteredWorkloads, workload)
		}
	}

	return filteredWorkloads, nil
}

// printJSONOutput prints workload information in JSON format
func printJSONOutput(workloadList []workloads.Workload) error {
	// Marshal to JSON
	jsonData, err := json.MarshalIndent(workloadList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON directly to stdout
	fmt.Println(string(jsonData))
	return nil
}

// printMCPServersOutput prints MCP servers configuration in JSON format
// This format is compatible with client configuration files
func printMCPServersOutput(workloadList []workloads.Workload) error {
	// Create a map to hold the MCP servers configuration
	mcpServers := make(map[string]map[string]string)

	for _, c := range workloadList {
		// Add the MCP server to the map
		mcpServers[c.Name] = map[string]string{
			"url":  c.URL,
			"type": c.TransportType.String(),
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

// printTextOutput prints workload information in text format
func printTextOutput(workloadList []workloads.Workload) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tPACKAGE\tSTATUS\tURL\tPORT\tTOOL TYPE\tGROUP\tCREATED AT")

	// Print workload information
	for _, c := range workloadList {
		// Print workload information
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			c.Name,
			c.Package,
			c.Status,
			c.URL,
			c.Port,
			c.ToolType,
			c.Group,
			c.CreatedAt,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Errorf("Warning: Failed to flush tabwriter: %v", err)
	}
}

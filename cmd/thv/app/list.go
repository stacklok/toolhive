package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/labels"
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
)

func init() {
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all workloads (default shows just running)")
	listCmd.Flags().StringVar(&listFormat, "format", FormatText, "Output format (json, text, or mcpservers)")
	listCmd.Flags().StringArrayVarP(&listLabelFilter, "label", "l", []string{}, "Filter workloads by labels (format: key=value)")
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Instantiate the status manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create status manager: %v", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, listAll)
	if err != nil {
		return fmt.Errorf("failed to list workloads: %v", err)
	}

	// Filter workloads by labels if specified
	if len(listLabelFilter) > 0 {
		filteredList, err := filterWorkloadsByLabels(workloadList, listLabelFilter)
		if err != nil {
			return fmt.Errorf("failed to filter workloads by labels: %v", err)
		}
		workloadList = filteredList
	}

	if len(workloadList) == 0 {
		fmt.Println("No MCP servers found")
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

// printTextOutput prints workload information in text format
func printTextOutput(workloadList []workloads.Workload) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tPACKAGE\tSTATUS\tURL\tPORT\tTOOL TYPE\tCREATED AT")

	// Print workload information
	for _, c := range workloadList {
		// Print workload information
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

// filterWorkloadsByLabels filters workloads based on label selectors
// TODO: Move this filtering to the server side (runtime layer) for better performance
func filterWorkloadsByLabels(workloadList []workloads.Workload, labelFilters []string) ([]workloads.Workload, error) {
	// Parse label filters
	filters := make(map[string]string)
	for _, filter := range labelFilters {
		key, value, err := labels.ParseLabel(filter)
		if err != nil {
			return nil, fmt.Errorf("invalid label filter '%s': %v", filter, err)
		}
		filters[key] = value
	}

	// Filter workloads
	var filtered []workloads.Workload
	for _, workload := range workloadList {
		if matchesLabelFilters(workload.Labels, filters) {
			filtered = append(filtered, workload)
		}
	}

	return filtered, nil
}

// matchesLabelFilters checks if workload labels match all the specified filters
func matchesLabelFilters(workloadLabels, filters map[string]string) bool {
	for filterKey, filterValue := range filters {
		workloadValue, exists := workloadLabels[filterKey]
		if !exists || workloadValue != filterValue {
			return false
		}
	}
	return true
}

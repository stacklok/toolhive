package app

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/client"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport"
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

// Constants for list command
const unknownTransport = "unknown"

// ContainerOutput represents container information for JSON output
type ContainerOutput struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	State     string `json:"state"`
	Transport string `json:"transport"`
	ToolType  string `json:"tool_type,omitempty"`
	Port      int    `json:"port"`
	URL       string `json:"url"`
}

func init() {
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all containers (default shows just running)")
	listCmd.Flags().StringVar(&listFormat, "format", "text", "Output format (json, text, or mcpservers)")
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	// Instantiate the container manager.
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	// Create container runtime
	toolHiveContainers, err := manager.ListContainers(ctx, listAll)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	if len(toolHiveContainers) == 0 {
		logger.Info("No MCP servers found")
		return nil
	}

	// Output based on format
	switch listFormat {
	//nolint:goconst
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
func printJSONOutput(containers []rt.ContainerInfo) error {
	var output []ContainerOutput

	for _, c := range containers {
		// Truncate container ID to first 12 characters (similar to Docker)
		truncatedID := c.ID
		if len(truncatedID) > 12 {
			truncatedID = truncatedID[:12]
		}

		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get transport type from labels
		t := labels.GetTransportType(c.Labels)
		if t == "" {
			t = unknownTransport
		}

		// Get tool type from labels
		toolType := labels.GetToolType(c.Labels)

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			port = 0
		}

		// Generate URL for the MCP server
		url := ""
		if port > 0 {
			url = client.GenerateMCPServerURL(transport.LocalhostIPv4, port, name)
		}

		output = append(output, ContainerOutput{
			ID:        truncatedID,
			Name:      name,
			Image:     c.Image,
			State:     c.State,
			Transport: t,
			ToolType:  toolType,
			Port:      port,
			URL:       url,
		})
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON
	logger.Info(string(jsonData))
	return nil
}

// printMCPServersOutput prints MCP servers configuration in JSON format
// This format is compatible with client configuration files
func printMCPServersOutput(containers []rt.ContainerInfo) error {
	// Create a map to hold the MCP servers configuration
	mcpServers := make(map[string]map[string]string)

	for _, c := range containers {
		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get tool type from labels
		toolType := labels.GetToolType(c.Labels)

		// Only include containers with tool type "mcp"
		if toolType != "mcp" {
			continue
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			port = 0
		}

		// Generate URL for the MCP server
		url := ""
		if port > 0 {
			url = client.GenerateMCPServerURL(transport.LocalhostIPv4, port, name)
		}

		// Add the MCP server to the map
		mcpServers[name] = map[string]string{
			"url": url,
		}
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(map[string]interface{}{
		"mcpServers": mcpServers,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON
	logger.Info(string(jsonData))
	return nil
}

// printTextOutput prints container information in text format
func printTextOutput(containers []rt.ContainerInfo) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "CONTAINER ID\tNAME\tIMAGE\tSTATE\tTRANSPORT\tPORT\tURL")

	// Print container information
	for _, c := range containers {
		// Truncate container ID to first 12 characters (similar to Docker)
		truncatedID := c.ID
		if len(truncatedID) > 12 {
			truncatedID = truncatedID[:12]
		}

		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get transport type from labels
		t := labels.GetTransportType(c.Labels)
		if t == "" {
			t = unknownTransport
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			port = 0
		}

		// Generate URL for the MCP server
		url := ""
		if port > 0 {
			url = client.GenerateMCPServerURL(transport.LocalhostIPv4, port, name)
		}

		// Print container information
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			truncatedID,
			name,
			c.Image,
			c.State,
			t,
			port,
			url,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Infof("Warning: Failed to flush tabwriter: %v", err)
	}
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/api/factory"
	"github.com/StacklokLabs/toolhive/pkg/client"
	"github.com/StacklokLabs/toolhive/pkg/logger"
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
const (
	defaultHost      = "localhost"
	unknownTransport = "unknown"
)

// ServerOutput represents server information for JSON output
type ServerOutput struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Image     string `json:"image"`
	State     string `json:"state"`
	Transport string `json:"transport"`
	Port      int    `json:"port"`
	URL       string `json:"url"`
}

func init() {
	listCmd.Flags().BoolVarP(&listAll, "all", "a", false, "Show all containers (default shows just running)")
	listCmd.Flags().StringVar(&listFormat, "format", "text", "Output format (json, text, or mcpservers)")
}

func listCmdFunc(cmd *cobra.Command, _ []string) error {
	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create API client factory
	apiFactory, err := factory.New(
		factory.WithClientType(factory.LocalClientType),
		factory.WithDebug(debugMode),
	)
	if err != nil {
		return fmt.Errorf("failed to create API client factory: %v", err)
	}

	// Create API client
	apiClient, err := apiFactory.Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create API client: %v", err)
	}
	defer apiClient.Close()

	// Create list options
	listOpts := &api.ListOptions{
		Status: "running",
	}
	if listAll {
		listOpts.Status = "all"
	}

	// List servers
	servers, err := apiClient.Server().List(ctx, listOpts)
	if err != nil {
		return fmt.Errorf("failed to list servers: %v", err)
	}

	if len(servers) == 0 {
		if listAll {
			fmt.Printf("No MCP servers found\n")
		} else {
			fmt.Printf("No running MCP servers found\n")
		}
		return nil
	}

	// Output based on format
	switch listFormat {
	//nolint:goconst
	case "json":
		return printJSONOutput(servers)
	case "mcpservers":
		return printMCPServersOutput(servers)
	default:
		printTextOutput(servers)
		return nil
	}
}

// printJSONOutput prints server information in JSON format
func printJSONOutput(servers []*api.Server) error {
	var output []ServerOutput

	for _, s := range servers {
		// Truncate container ID to first 12 characters (similar to Docker)
		truncatedID := s.ContainerID
		if len(truncatedID) > 12 {
			truncatedID = truncatedID[:12]
		}

		// Get state from status
		state := string(s.Status)

		// Generate URL for the MCP server
		url := ""
		if s.HostPort > 0 {
			url = client.GenerateMCPServerURL(defaultHost, s.HostPort, s.Name)
		}

		output = append(output, ServerOutput{
			ID:        truncatedID,
			Name:      s.Name,
			Image:     s.Image,
			State:     state,
			Transport: s.Transport,
			Port:      s.HostPort,
			URL:       url,
		})
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %v", err)
	}

	// Print JSON
	logger.Log.Infof(string(jsonData))
	return nil
}

// printMCPServersOutput prints MCP servers configuration in JSON format
// This format is compatible with client configuration files
func printMCPServersOutput(servers []*api.Server) error {
	// Create a map to hold the MCP servers configuration
	mcpServers := make(map[string]map[string]string)

	for _, s := range servers {
		// Only include running servers
		if s.Status != api.ServerStatusRunning {
			continue
		}

		// Generate URL for the MCP server
		url := ""
		if s.HostPort > 0 {
			url = client.GenerateMCPServerURL(defaultHost, s.HostPort, s.Name)
		}

		// Add the MCP server to the map
		mcpServers[s.Name] = map[string]string{
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
	logger.Log.Infof(string(jsonData))
	return nil
}

// printTextOutput prints server information in text format
func printTextOutput(servers []*api.Server) {
	// Create a tabwriter for pretty output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "CONTAINER ID\tNAME\tIMAGE\tSTATE\tTRANSPORT\tPORT\tURL")

	// Print server information
	for _, s := range servers {
		// Truncate container ID to first 12 characters (similar to Docker)
		truncatedID := s.ContainerID
		if len(truncatedID) > 12 {
			truncatedID = truncatedID[:12]
		}

		// Get state from status
		state := string(s.Status)

		// Generate URL for the MCP server
		url := ""
		if s.HostPort > 0 {
			url = client.GenerateMCPServerURL(defaultHost, s.HostPort, s.Name)
		}

		// Print server information
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			truncatedID,
			s.Name,
			s.Image,
			state,
			s.Transport,
			s.HostPort,
			url,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		logger.Log.Infof("Warning: Failed to flush tabwriter: %v", err)
	}
}

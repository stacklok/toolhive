// Package main provides the entry point for the vibetool command-line application.
// This file contains the implementation of the 'list' command.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/client"
	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/labels"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List running MCP servers",
	Long:  `List all MCP servers managed by Vibe Tool, including their status and configuration.`,
	RunE:  listCmdFunc,
}

var (
	listAll    bool
	listFormat string
)

// ContainerOutput represents container information for JSON output
type ContainerOutput struct {
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
	listCmd.Flags().StringVar(&listFormat, "format", "text", "Output format (json or text)")
}

func listCmdFunc(_ *cobra.Command, _ []string) error {
	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// List containers
	containers, err := runtime.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by Vibe Tool
	var vibeToolContainers []container.ContainerInfo
	for _, c := range containers {
		if labels.IsVibeToolContainer(c.Labels) {
			vibeToolContainers = append(vibeToolContainers, c)
		}
	}

	// Filter containers if not showing all
	if !listAll {
		var runningContainers []container.ContainerInfo
		for _, c := range vibeToolContainers {
			if c.State == "running" {
				runningContainers = append(runningContainers, c)
			}
		}
		vibeToolContainers = runningContainers
	}

	if len(vibeToolContainers) == 0 {
		fmt.Println("No MCP servers found")
		return nil
	}

	// Output based on format
	switch listFormat {
	//nolint:goconst
	case "json":
		return printJSONOutput(vibeToolContainers)
	default:
		printTextOutput(vibeToolContainers)
		return nil
	}
}

// printJSONOutput prints container information in JSON format
func printJSONOutput(containers []container.ContainerInfo) error {
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
		transport := labels.GetTransportType(c.Labels)
		if transport == "" {
			transport = "unknown"
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			port = 0
		}

		// Generate URL for the MCP server
		host := "localhost" // Default to localhost
		url := ""
		if port > 0 {
			url = client.GenerateMCPServerURL(host, port, name)
		}

		output = append(output, ContainerOutput{
			ID:        truncatedID,
			Name:      name,
			Image:     c.Image,
			State:     c.State,
			Transport: transport,
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
	fmt.Println(string(jsonData))
	return nil
}

// printTextOutput prints container information in text format
func printTextOutput(containers []container.ContainerInfo) {
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
		transport := labels.GetTransportType(c.Labels)
		if transport == "" {
			transport = "unknown"
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			port = 0
		}

		// Generate URL for the MCP server
		host := "localhost" // Default to localhost
		url := ""
		if port > 0 {
			url = client.GenerateMCPServerURL(host, port, name)
		}

		// Print container information
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			truncatedID,
			name,
			c.Image,
			c.State,
			transport,
			port,
			url,
		)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		fmt.Printf("Warning: Failed to flush tabwriter: %v\n", err)
	}
}

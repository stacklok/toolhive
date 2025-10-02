package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/export"
	"github.com/stacklok/toolhive/pkg/runner"
)

var exportFormat string

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <workload name> <path>",
		Short: "Export a workload's run configuration to a file",
		Long: `Export a workload's run configuration to a file for sharing or backup.

The exported configuration can be used with 'thv run --from-config <path>' to recreate
the same workload with identical settings.

You can export in different formats:
- json: Export as RunConfig JSON (default, can be used with 'thv run --from-config')
- k8s: Export as Kubernetes MCPServer resource YAML

Examples:

	# Export a workload configuration to a JSON file
	thv export my-server ./my-server-config.json

	# Export as Kubernetes MCPServer resource
	thv export my-server ./my-server.yaml --format k8s

	# Export to a specific directory
	thv export github-mcp /tmp/configs/github-config.json`,
		Args: cobra.ExactArgs(2),
		RunE: exportCmdFunc,
	}

	cmd.Flags().StringVar(&exportFormat, "format", "json", "Export format: json or k8s")

	return cmd
}

func exportCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	workloadName := args[0]
	outputPath := args[1]

	// Validate format
	if exportFormat != "json" && exportFormat != "k8s" {
		return fmt.Errorf("invalid format '%s': must be 'json' or 'k8s'", exportFormat)
	}

	// Load the saved run configuration
	runConfig, err := runner.LoadState(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to load run configuration for workload '%s': %w", workloadName, err)
	}

	// Ensure the output directory exists
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Create the output file
	// #nosec G304 - outputPath is provided by the user as a command line argument for export functionality
	outputFile, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outputFile.Close()

	// Write the configuration based on format
	switch exportFormat {
	case "json":
		if err := runConfig.WriteJSON(outputFile); err != nil {
			return fmt.Errorf("failed to write configuration to file: %w", err)
		}
		fmt.Printf("Successfully exported run configuration for '%s' to '%s'\n", workloadName, outputPath)
	case "k8s":
		if err := export.WriteK8sManifest(runConfig, outputFile); err != nil {
			return fmt.Errorf("failed to write Kubernetes manifest: %w", err)
		}
		fmt.Printf("Successfully exported Kubernetes MCPServer resource for '%s' to '%s'\n", workloadName, outputPath)
	}

	return nil
}

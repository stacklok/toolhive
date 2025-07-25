package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/runner"
)

func newExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <workload name> <path>",
		Short: "Export a workload's run configuration to a file",
		Long: `Export a workload's run configuration to a file for sharing or backup.

	The exported configuration can be used with 'thv run --from-config <path>' to recreate
	the same workload with identical settings.

	Examples:
	# Export a workload configuration to a file
	thv export my-server ./my-server-config.json

	# Export to a specific directory
	thv export github-mcp /tmp/configs/github-config.json`,
		Args: cobra.ExactArgs(2),
		RunE: exportCmdFunc,
	}
}

func exportCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	workloadName := args[0]
	outputPath := args[1]

	// Load the saved run configuration
	runnerInstance, err := runner.LoadState(ctx, workloadName)
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

	// Write the configuration to the file
	if err := runnerInstance.Config.WriteJSON(outputFile); err != nil {
		return fmt.Errorf("failed to write configuration to file: %w", err)
	}

	fmt.Printf("Successfully exported run configuration for '%s' to '%s'\n", workloadName, outputPath)
	return nil
}

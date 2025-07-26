package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container/images"
	"github.com/stacklok/toolhive/pkg/oci"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/export"
)

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <workload name> <path|oci-reference>",
		Short: "Export a workload's run configuration to a file or OCI artifact",
		Long: `Export a workload's run configuration to a file or OCI artifact for sharing or backup.

	The exported configuration can be used with 'thv run --from-config <path>' or
	'thv run --from-config <oci-reference>' to recreate the same workload with identical settings.

	When exporting to an OCI reference, the artifact is pushed to the remote registry.
	When exporting to a file path, the configuration is saved as a JSON file.

	Examples:
	# Export a workload configuration to a file
	thv export my-server ./my-server-config.json

	# Export to a specific directory
	thv export github-mcp /tmp/configs/github-config.json

	# Export to an OCI artifact (pushes to registry)
	thv export my-server registry.example.com/configs/my-server:latest`,
		Args: cobra.ExactArgs(2),
		RunE: exportCmdFunc,
	}

	return cmd
}

func exportCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	workloadName := args[0]
	destination := args[1]

	// Load the saved run configuration
	runnerInstance, err := runner.LoadState(ctx, workloadName)
	if err != nil {
		return fmt.Errorf("failed to load run configuration for workload '%s': %w", workloadName, err)
	}

	// Determine if destination is an OCI reference or file path
	if oci.IsOCIReference(destination) {
		return exportToOCI(ctx, runnerInstance.Config, workloadName, destination)
	}

	return exportToFile(runnerInstance.Config, workloadName, destination)
}

// exportToOCI exports the configuration to an OCI artifact
func exportToOCI(ctx context.Context, config *runner.RunConfig, workloadName, ref string) error {
	// Validate the OCI reference early
	if err := oci.ValidateReference(ref); err != nil {
		return fmt.Errorf("invalid OCI reference: %w", err)
	}

	imageManager := images.NewImageManager(ctx)
	ociClient := oci.NewClient(imageManager)
	exporter := export.NewOCIExporter(ociClient)

	// Push directly to registry (no local storage for OCI artifacts)
	if err := exporter.PushRunConfig(ctx, config, ref); err != nil {
		return fmt.Errorf("failed to push configuration to OCI registry: %w", err)
	}
	fmt.Printf("Successfully exported run configuration for '%s' to OCI registry '%s'\n", workloadName, ref)

	return nil
}

// exportToFile exports the configuration to a local file
func exportToFile(config *runner.RunConfig, workloadName, outputPath string) error {
	// Validate input
	if config == nil {
		return fmt.Errorf("configuration cannot be nil")
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
	if err := config.WriteJSON(outputFile); err != nil {
		return fmt.Errorf("failed to write configuration to file: %w", err)
	}

	fmt.Printf("Successfully exported run configuration for '%s' to file '%s'\n", workloadName, outputPath)
	return nil
}

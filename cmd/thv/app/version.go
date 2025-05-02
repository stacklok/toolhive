package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/api/factory"
)

// newVersionCmd creates a new version command
func newVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show the version of ToolHive",
		Long:  `Display detailed version information about ToolHive, including version number, git commit, build date, and Go version.`,
		RunE:  versionCmdFunc,
	}

	cmd.Flags().String("format", "text", "Output format (json or text)")

	return cmd
}

func versionCmdFunc(cmd *cobra.Command, _ []string) error {
	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Get format flag
	format, _ := cmd.Flags().GetString("format")

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

	// Create version options
	versionOpts := &api.VersionOptions{
		Format: format,
	}

	// Get version information
	versionInfo, err := apiClient.Version().Get(ctx, versionOpts)
	if err != nil {
		return fmt.Errorf("failed to get version information: %v", err)
	}

	// Print version information
	fmt.Println(versionInfo)

	return nil
}

package app

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
)

var (
	followFlag bool
)

func logsCommand() *cobra.Command {
	logsCommand := &cobra.Command{
		Use:   "logs [container-name]",
		Short: "Output the logs of an MCP server",
		Long:  `Output the logs of an MCP server managed by ToolHive.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return logsCmdFunc(cmd, args)
		},
	}

	logsCommand.Flags().BoolVarP(&followFlag, "follow", "f", false, "Follow log output")
	err := viper.BindPFlag("follow", logsCommand.Flags().Lookup("follow"))
	if err != nil {
		logger.Errorf("failed to bind flag: %v", err)
	}

	return logsCommand
}

func logsCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get container name
	containerName := args[0]

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// List workloads to find the one with the given name
	containers, err := runtime.ListWorkloads(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Find the container with the given name
	var containerID string
	for _, c := range containers {
		// Check if the container is managed by ToolHive
		if !labels.IsToolHiveContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if name == containerName || strings.HasPrefix(c.ID, containerName) {
			containerID = c.ID
			break
		}
	}

	if containerID == "" {
		logger.Infof("container %s not found", containerName)
		return nil
	}

	follow := viper.GetBool("follow")
	logs, err := runtime.GetWorkloadLogs(ctx, containerID, follow)
	if err != nil {
		return fmt.Errorf("failed to get container logs: %v", err)
	}
	fmt.Print(logs)
	return nil
}

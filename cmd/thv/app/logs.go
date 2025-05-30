package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
)

var (
	followFlag bool
)

func logsCommand() *cobra.Command {
	logsCommand := &cobra.Command{
		Use:   "logs [container-name|prune]",
		Short: "Output the logs of an MCP server or manage log files",
		Long:  `Output the logs of an MCP server managed by ToolHive, or manage log files.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if the argument is "prune"
			if args[0] == "prune" {
				return logsPruneCmdFunc(cmd)
			}
			return logsCmdFunc(cmd, args)
		},
	}

	logsCommand.Flags().BoolVarP(&followFlag, "follow", "f", false, "Follow log output (only for container logs)")
	err := viper.BindPFlag("follow", logsCommand.Flags().Lookup("follow"))
	if err != nil {
		logger.Errorf("failed to bind flag: %v", err)
	}

	// Add prune subcommand for better discoverability
	pruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete log files from servers not currently managed by ToolHive",
		Long: `Delete log files from servers that are not currently managed by ToolHive (running or stopped).
This helps clean up old log files that accumulate over time from removed servers.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return logsPruneCmdFunc(cmd)
		},
	}
	logsCommand.AddCommand(pruneCmd)

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

func logsPruneCmdFunc(cmd *cobra.Command) error {
	ctx := cmd.Context()

	logsDir, err := getLogsDirectory()
	if err != nil {
		return err
	}

	managedNames, err := getManagedContainerNames(ctx)
	if err != nil {
		return err
	}

	logFiles, err := getLogFiles(logsDir)
	if err != nil {
		return err
	}

	if len(logFiles) == 0 {
		logger.Info("No log files found")
		return nil
	}

	prunedFiles, errors := pruneOrphanedLogFiles(logFiles, managedNames)
	reportPruneResults(prunedFiles, errors)

	return nil
}

func getLogsDirectory() (string, error) {
	logsDir, err := xdg.DataFile("toolhive/logs")
	if err != nil {
		return "", fmt.Errorf("failed to get logs directory path: %v", err)
	}

	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		logger.Info("No logs directory found, nothing to prune")
		return "", nil
	}

	return logsDir, nil
}

func getManagedContainerNames(ctx context.Context) (map[string]bool, error) {
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create container manager: %v", err)
	}

	managedContainers, err := manager.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	managedNames := make(map[string]bool)
	for _, c := range managedContainers {
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}
		if name != "" {
			managedNames[name] = true
		}
	}

	return managedNames, nil
}

func getLogFiles(logsDir string) ([]string, error) {
	if logsDir == "" {
		return nil, nil
	}

	logFiles, err := filepath.Glob(filepath.Join(logsDir, "*.log"))
	if err != nil {
		return nil, fmt.Errorf("failed to list log files: %v", err)
	}

	return logFiles, nil
}

func pruneOrphanedLogFiles(logFiles []string, managedNames map[string]bool) ([]string, []string) {
	var prunedFiles []string
	var errors []string

	for _, logFile := range logFiles {
		baseName := strings.TrimSuffix(filepath.Base(logFile), ".log")

		if !managedNames[baseName] {
			if err := os.Remove(logFile); err != nil {
				errors = append(errors, fmt.Sprintf("failed to remove %s: %v", logFile, err))
				logger.Warnf("Failed to remove log file %s: %v", logFile, err)
			} else {
				prunedFiles = append(prunedFiles, logFile)
				logger.Infof("Removed log file: %s", logFile)
			}
		}
	}

	return prunedFiles, errors
}

func reportPruneResults(prunedFiles, errors []string) {
	if len(prunedFiles) == 0 {
		logger.Info("No orphaned log files found to prune")
	} else {
		logger.Infof("Successfully pruned %d log file(s)", len(prunedFiles))
		for _, file := range prunedFiles {
			fmt.Printf("Removed: %s\n", file)
		}
	}

	if len(errors) > 0 {
		logger.Warnf("Encountered %d error(s) during pruning:", len(errors))
		for _, errMsg := range errors {
			fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
		}
	}
}

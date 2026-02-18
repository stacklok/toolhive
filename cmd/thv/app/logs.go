// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/adrg/xdg"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	followFlag bool
	proxyFlag  bool
)

func logsCommand() *cobra.Command {
	logsCommand := &cobra.Command{
		Use:   "logs [workload-name|prune]",
		Short: "Output the logs of an MCP server or manage log files",
		Long: `Output the logs of an MCP server managed by ToolHive, or manage log files.

By default, this command shows the logs from the MCP server container.
Use --proxy to view the logs from the ToolHive proxy process instead.

Examples:
  # View logs of an MCP server
  thv logs filesystem

  # Follow logs in real-time
  thv logs filesystem --follow

  # View proxy logs instead of container logs
  thv logs filesystem --proxy

  # Clean up old log files
  thv logs prune`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if the argument is "prune"
			if args[0] == "prune" {
				return logsPruneCmdFunc(cmd)
			}
			return logsCmdFunc(cmd, args)
		},
		ValidArgsFunction: completeLogsArgs,
	}

	logsCommand.Flags().BoolVarP(&followFlag, "follow", "f", false, "Follow log output (only for workload logs) (default false)")
	logsCommand.Flags().BoolVarP(&proxyFlag, "proxy", "p", false, "Show proxy logs instead of container logs (default false)")

	err := viper.BindPFlag("follow", logsCommand.Flags().Lookup("follow"))
	if err != nil {
		slog.Error(fmt.Sprintf("failed to bind flag: %v", err))
	}

	err = viper.BindPFlag("proxy", logsCommand.Flags().Lookup("proxy"))
	if err != nil {
		slog.Error(fmt.Sprintf("failed to bind flag: %v", err))
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
	// Get workload name
	workloadName := args[0]
	follow := viper.GetBool("follow")
	proxy := viper.GetBool("proxy")

	if follow {
		var cancel context.CancelFunc
		ctx, cancel = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
	}

	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	if proxy {
		if follow {
			return getProxyLogs(ctx, workloadName)
		}
		// Use the shared manager method for non-follow proxy logs
		// CLI gets all logs (0 = unlimited)
		logs, err := manager.GetProxyLogs(ctx, workloadName, 0)
		if err != nil {
			slog.Info(fmt.Sprintf("Proxy logs not found for workload %s", workloadName))
			return nil
		}
		fmt.Print(logs)
		return nil
	}

	// CLI gets all logs (0 = unlimited)
	logs, err := manager.GetLogs(ctx, workloadName, follow, 0)
	if err != nil {
		if errors.Is(err, rt.ErrWorkloadNotFound) {
			return fmt.Errorf("container logs for workload %s not found, use --proxy to get proxy logs", workloadName)
		}
		return fmt.Errorf("failed to get logs for workload %s: %w", workloadName, err)
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
		fmt.Println("No log files found")
		return nil
	}

	prunedFiles, errs := pruneOrphanedLogFiles(logFiles, managedNames)
	reportPruneResults(prunedFiles, errs)

	return nil
}

func getLogsDirectory() (string, error) {
	logsDir, err := xdg.DataFile("toolhive/logs")
	if err != nil {
		return "", fmt.Errorf("failed to get logs directory path: %w", err)
	}

	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		fmt.Println("No logs directory found, nothing to prune")
		return "", nil
	}

	return logsDir, nil
}

func getManagedContainerNames(ctx context.Context) (map[string]bool, error) {
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create status manager: %w", err)
	}

	managedContainers, err := manager.ListWorkloads(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads: %w", err)
	}

	managedNames := make(map[string]bool)
	for _, c := range managedContainers {
		name := c.Name
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
		return nil, fmt.Errorf("failed to list log files: %w", err)
	}

	return logFiles, nil
}

func pruneOrphanedLogFiles(logFiles []string, managedNames map[string]bool) ([]string, []string) {
	var prunedFiles []string
	var errs []string

	for _, logFile := range logFiles {
		baseName := strings.TrimSuffix(filepath.Base(logFile), ".log")

		if !managedNames[baseName] {
			if err := os.Remove(logFile); err != nil {
				errs = append(errs, fmt.Sprintf("failed to remove %s: %v", logFile, err))
				slog.Warn(fmt.Sprintf("Failed to remove log file %s: %v", logFile, err))
			} else {
				prunedFiles = append(prunedFiles, logFile)
				slog.Debug(fmt.Sprintf("Removed log file: %s", logFile))
			}
		}
	}

	return prunedFiles, errs
}

func reportPruneResults(prunedFiles, errs []string) {
	if len(prunedFiles) == 0 {
		fmt.Println("No orphaned log files found to prune")
	} else {
		slog.Debug(fmt.Sprintf("Successfully pruned %d log file(s)", len(prunedFiles)))
		for _, file := range prunedFiles {
			fmt.Printf("Removed: %s\n", file)
		}
	}

	if len(errs) > 0 {
		slog.Warn(fmt.Sprintf("Encountered %d error(s) during pruning:", len(errs)))
		for _, errMsg := range errs {
			fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
		}
	}
}

// getProxyLogs reads and displays the proxy logs for a given workload in follow mode
func getProxyLogs(ctx context.Context, workloadName string) error {
	// Get the proxy log file path
	logFilePath, err := xdg.DataFile(fmt.Sprintf("toolhive/logs/%s.log", workloadName))
	if err != nil {
		return fmt.Errorf("failed to get proxy log file path: %w", err)
	}

	// Clean the file path to prevent path traversal
	cleanLogFilePath := filepath.Clean(logFilePath)

	// Check if the log file exists
	if _, err := os.Stat(cleanLogFilePath); os.IsNotExist(err) {
		slog.Info(fmt.Sprintf("proxy log not found for workload %s", workloadName))
		return nil
	}

	return followProxyLogFile(ctx, cleanLogFilePath)
}

// followProxyLogFile implements tail -f functionality for proxy logs
func followProxyLogFile(ctx context.Context, logFilePath string) error {
	// Clean the file path to prevent path traversal
	cleanLogFilePath := filepath.Clean(logFilePath)

	// Open the file
	file, err := os.Open(cleanLogFilePath)
	if err != nil {
		return fmt.Errorf("failed to open proxy log %s: %w", cleanLogFilePath, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			// Non-fatal: file cleanup failure after reading
			slog.Warn(fmt.Sprintf("Failed to close log file: %v", err))
		}
	}()

	// Read existing content first
	content, err := os.ReadFile(cleanLogFilePath)
	if err == nil {
		fmt.Print(string(content))
	}

	// Seek to the end of the file for following
	_, err = file.Seek(0, 2)
	if err != nil {
		return fmt.Errorf("failed to seek to end of proxy log: %w", err)
	}

	// Follow the file for new content
	contentCheckInterval := 100 * time.Millisecond

	ticker := time.NewTicker(contentCheckInterval)
	defer ticker.Stop()

	for {
		// Read any new content
		buffer := make([]byte, 1024)
		n, err := file.Read(buffer)
		if err != nil && err.Error() != "EOF" {
			return fmt.Errorf("error reading proxy log: %w", err)
		}

		if n > 0 {
			fmt.Print(string(buffer[:n]))
		}

		// Wait for next iteration or cancellation
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Continue to next iteration
		}
	}
}

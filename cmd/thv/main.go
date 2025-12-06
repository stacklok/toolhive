// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adrg/xdg"

	"github.com/stacklok/toolhive/cmd/thv/app"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/lockfile"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/migration"
)

func main() {
	// Initialize the logger
	logger.Initialize()

	// Setup signal handling for graceful cleanup
	setupSignalHandler()

	// Clean up stale lock files on startup
	cleanupStaleLockFiles()

	// Check if container runtime is available early, but skip for informational commands
	if !app.IsInformationalCommand(os.Args) {
		if err := container.CheckRuntimeAvailable(); err != nil {
			logger.Errorf("%s", err.Error())
			os.Exit(1)
		}
	}

	// Skip migrations for informational commands that don't need container runtime
	if !app.IsInformationalCommand(os.Args) {
		// Check and perform auto-discovery migration if needed
		// Handles the auto-discovery flag depreciation, only executes once on old config files
		client.CheckAndPerformAutoDiscoveryMigration()

		// Check and perform default group migration if needed
		// Migrates existing workloads to the default group, only executes once
		migration.CheckAndPerformDefaultGroupMigration()
	}

	// Skip update check for completion command or if we are running in kubernetes
	if err := app.NewRootCmd(!app.IsCompletionCommand(os.Args) && !runtime.IsKubernetesRuntime()).Execute(); err != nil {
		// Clean up any remaining lock files on error exit
		lockfile.CleanupAllLocks()
		os.Exit(1)
	}

	// Clean up lock files on normal exit
	lockfile.CleanupAllLocks()
}

// setupSignalHandler configures signal handling to ensure lock files are cleaned up
func setupSignalHandler() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	go func() {
		<-sigCh
		logger.Debugf("Received signal, cleaning up lock files...")
		lockfile.CleanupAllLocks()
		os.Exit(0)
	}()
}

// cleanupStaleLockFiles removes stale lock files from known directories on startup
func cleanupStaleLockFiles() {
	// Common directories where lock files are created
	var directories []string

	// Config directory
	if configDir, err := xdg.ConfigFile("toolhive"); err == nil {
		directories = append(directories, configDir)
	}

	// Data directory (for statuses and updates)
	if dataDir, err := xdg.DataFile("toolhive"); err == nil {
		directories = append(directories, dataDir)

		// Specific subdirectories
		if statusDir, err := xdg.DataFile("toolhive/statuses"); err == nil {
			directories = append(directories, statusDir)
		}
	}

	// Clean up lock files older than 5 minutes (should be safe for most operations)
	lockfile.CleanupStaleLocks(directories, 5*time.Minute)
}

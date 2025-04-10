// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	"github.com/StacklokLabs/toolhive/cmd/thv/app"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/updates"
)

func main() {
	// Initialize the logger system
	logger.Initialize()

	checkForUpdates()

	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Log.Error("%v, %v", os.Stderr, err)
		os.Exit(1)
	}
}

func checkForUpdates() {
	versionClient := updates.NewVersionClient()
	updateChecker, err := updates.NewUpdateChecker(versionClient)
	// treat update-related errors as non-fatal
	if err != nil {
		logger.Log.Error("unable to create update client: %w", err)
		return
	}

	err = updateChecker.CheckLatestVersion()
	if err != nil {
		logger.Log.Error("error while checking for updates: %w", err)
	}
}

// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	"github.com/stacklok/toolhive/cmd/thv/app"
	"github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger
	logger.Initialize()

	if err := app.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

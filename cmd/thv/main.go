// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	"github.com/StacklokLabs/toolhive/cmd/thv/app"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger system
	logger.Initialize()

	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Log.Error("%v, %v", os.Stderr, err)
		os.Exit(1)
	}
}

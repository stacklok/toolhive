// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	cli "github.com/StacklokLabs/toolhive/cmd/thv/app"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger system
	logger.Initialize()

	if err := cli.NewRootCmd().Execute(); err != nil {
		logger.Log.Error("%v, %v", os.Stderr, err)
		os.Exit(1)
	}
}

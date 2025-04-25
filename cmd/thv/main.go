// Package main is the entry point for the ToolHive CLI.
package main

import (
	"os"

	"github.com/StacklokLabs/toolhive/cmd/thv/app"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

func main() {
	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Log.Errorf("%v", err)
		os.Exit(1)
	}
}

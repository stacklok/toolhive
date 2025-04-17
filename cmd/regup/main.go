// Package main is the entry point for the regup command
package main

import (
	"os"

	"github.com/StacklokLabs/toolhive/cmd/regup/app"
	"github.com/StacklokLabs/toolhive/pkg/logger"
)

func main() {
	// Initialize the logger system
	logger.Initialize()

	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Log.Errorf("%v", err)
		os.Exit(1)
	}
}

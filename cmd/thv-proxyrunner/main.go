// Package main is the entry point for the ToolHive ProxyRunner.
package main

import (
	"os"

	"github.com/stacklok/toolhive/cmd/thv-proxyrunner/app"
)

func main() {
	// Skip update check for completion command or if we are running in kubernetes
	if err := app.NewRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// Package main is the entry point for the ToolHive CLI.
package main

import (
	"fmt"
	"os"

	"github.com/StacklokLabs/toolhive/cmd/thv/app"
)

func main() {
	if err := app.NewRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "there was an error: %v\n", err)
		os.Exit(1)
	}
}

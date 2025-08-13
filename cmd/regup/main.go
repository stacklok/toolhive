// Package main is the entry point for the regup command
package main

import (
	"os"

	"github.com/stacklok/toolhive/cmd/regup/app"
	log "github.com/stacklok/toolhive/pkg/logger"
)

func main() {
	logger := log.NewLogger()

	if err := app.NewRootCmd().Execute(); err != nil {
		logger.Errorf("%v", err)
		os.Exit(1)
	}
}

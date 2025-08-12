// Package migration handles any migrations needed to maintain compatibility
package migration

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

// migrationOnce ensures the migration only runs once
var migrationOnce sync.Once

// CheckAndPerformDefaultGroupMigration checks if default group migration is needed and performs it
// This is called once at application startup
func CheckAndPerformDefaultGroupMigration() {
	migrationOnce.Do(func() {
		appConfig := config.GetConfig()

		// Check if default group migration has already been performed
		if appConfig.DefaultGroupMigration {
			return
		}

		if err := performDefaultGroupMigration(); err != nil {
			logger.Errorf("Failed to perform default group migration: %v", err)
			return
		}
	})
}

// performDefaultGroupMigration migrates all existing workloads to the default group
func performDefaultGroupMigration() error {
	fmt.Println("Migrating existing workloads to default group...")
	fmt.Println()

	migrator := &DefaultGroupMigrator{}
	return migrator.Migrate(context.Background())
}

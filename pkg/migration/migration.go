// Package migration handles any migrations needed to maintain compatibility
package migration

import (
	"context"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

// migrationOnce ensures the migration only runs once
var migrationOnce sync.Once

// CheckAndPerformDefaultGroupMigration checks if default group migration is needed and performs it
// This is called once at application startup
func CheckAndPerformDefaultGroupMigration() {
	migrationOnce.Do(func() {
		if err := performDefaultGroupMigration(); err != nil {
			logger.Errorf("Failed to perform default group migration: %v", err)
			return
		}
	})
}

// performDefaultGroupMigration migrates all existing workloads to the default group
func performDefaultGroupMigration() error {
	migrator := &DefaultGroupMigrator{}
	return migrator.Migrate(context.Background())
}

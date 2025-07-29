package groups

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/state"
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

		performDefaultGroupMigration()
	})
}

// performDefaultGroupMigration migrates all existing workloads to the default group
func performDefaultGroupMigration() {
	fmt.Println("Migrating existing workloads to default group...")
	fmt.Println()

	// Create group manager and ensure default group exists
	groupManager, err := NewManager()
	if err != nil {
		logger.Errorf("Failed to create group manager: %v", err)
		return
	}

	// Create default group
	if err := createDefaultGroup(context.Background(), groupManager); err != nil {
		logger.Errorf("Failed to create default group: %v", err)
		return
	}

	// Create a runconfig store to list all runconfigs
	runConfigStore, err := state.NewRunConfigStore("toolhive")
	if err != nil {
		logger.Errorf("Failed to create runconfig store: %v", err)
		return
	}

	// List all runconfig names
	runConfigNames, err := runConfigStore.List(context.Background())
	if err != nil {
		logger.Errorf("Failed to list runconfigs: %v", err)
		return
	}

	migratedCount := 0
	for _, runConfigName := range runConfigNames {
		// Load the runconfig
		runnerInstance, err := runner.LoadState(context.Background(), runConfigName)
		if err != nil {
			// Log the error but continue processing other runconfigs
			logger.Warnf("Failed to load runconfig %s: %v", runConfigName, err)
			continue
		}

		// If the workload has no group, assign it to the default group
		if runnerInstance.Config.Group == "" {
			runnerInstance.Config.Group = DefaultGroupName
			if err := runnerInstance.SaveState(context.Background()); err != nil {
				logger.Warnf("Failed to save runconfig for %s: %v", runConfigName, err)
				continue
			}
			migratedCount++
		}
	}

	if migratedCount > 0 {
		fmt.Printf("\nSuccessfully migrated %d workloads to default group '%s'\n", migratedCount, DefaultGroupName)
	} else {
		fmt.Println("No workloads needed migration to default group")
	}

	// Mark default group migration as completed
	err = config.UpdateConfig(func(c *config.Config) {
		c.DefaultGroupMigration = true
	})

	if err != nil {
		logger.Errorf("Error updating config during migration: %v", err)
		return
	}
}

// createDefaultGroup creates the default group if it doesn't exist
func createDefaultGroup(ctx context.Context, groupManager Manager) error {
	logger.Infof("Creating default group '%s'", DefaultGroupName)
	if err := groupManager.Create(ctx, DefaultGroupName); err != nil {
		return fmt.Errorf("failed to create default group: %w", err)
	}
	return nil
}

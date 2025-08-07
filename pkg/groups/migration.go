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
		runConfig, err := runner.LoadState(context.Background(), runConfigName)
		if err != nil {
			// Log the error but continue processing other runconfigs
			logger.Warnf("Failed to load runconfig %s: %v", runConfigName, err)
			continue
		}

		// If the workload has no group, assign it to the default group
		if runConfig.Group == "" {
			runConfig.Group = DefaultGroupName
			if err := runConfig.SaveState(context.Background()); err != nil {
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

	// Migrate client configurations from global config to default group
	if err := migrateClientConfigs(context.Background(), groupManager); err != nil {
		logger.Errorf("Failed to migrate client configurations: %v", err)
		return
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

// migrateClientConfigs migrates client configurations from global config to default group
func migrateClientConfigs(ctx context.Context, groupManager Manager) error {
	appConfig := config.GetConfig()

	// If there are no registered clients, nothing to migrate
	if len(appConfig.Clients.RegisteredClients) == 0 {
		logger.Infof("No client configurations to migrate")
		return nil
	}

	fmt.Printf("Migrating %d client configurations to default group...\n", len(appConfig.Clients.RegisteredClients))

	// Get the default group
	defaultGroup, err := groupManager.Get(ctx, DefaultGroupName)
	if err != nil {
		return fmt.Errorf("failed to get default group: %w", err)
	}

	migratedCount := 0
	// Copy all registered clients to the default group
	for _, clientName := range appConfig.Clients.RegisteredClients {
		// Check if client is already in the group (avoid duplicates)
		alreadyRegistered := false
		for _, existingClient := range defaultGroup.RegisteredClients {
			if existingClient == clientName {
				alreadyRegistered = true
				break
			}
		}

		if !alreadyRegistered {
			if err := groupManager.RegisterClient(ctx, DefaultGroupName, clientName); err != nil {
				logger.Warnf("Failed to register client %s to default group: %v", clientName, err)
				continue
			}
			migratedCount++
		}
	}

	if migratedCount > 0 {
		fmt.Printf("Successfully migrated %d client configurations to default group '%s'\n", migratedCount, DefaultGroupName)

		// Clear the global client configurations after successful migration
		err = config.UpdateConfig(func(c *config.Config) {
			c.Clients.RegisteredClients = []string{}
		})
		if err != nil {
			logger.Warnf("Failed to clear global client configurations after migration: %v", err)
		} else {
			logger.Infof("Cleared global client configurations")
		}
	} else {
		logger.Infof("No client configurations needed migration")
	}

	return nil
}

// createDefaultGroup creates the default group if it doesn't exist
func createDefaultGroup(ctx context.Context, groupManager Manager) error {
	logger.Infof("Creating default group '%s'", DefaultGroupName)
	if err := groupManager.Create(ctx, DefaultGroupName); err != nil {
		return fmt.Errorf("failed to create default group: %w", err)
	}
	return nil
}

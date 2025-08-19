package migration

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// DefaultGroupMigrator handles the migration of workloads and clients to the default group
type DefaultGroupMigrator struct {
	groupManager     groups.Manager
	workloadsManager workloads.Manager
}

// Migrate performs the complete default group migration
func (m *DefaultGroupMigrator) Migrate(ctx context.Context) error {
	if err := m.initManagers(ctx); err != nil {
		return fmt.Errorf("failed to initialize managers: %w", err)
	}

	// Create default group if it doesn't exist
	defaultGroupExists, err := m.groupManager.Exists(ctx, groups.DefaultGroupName)
	if err != nil {
		return fmt.Errorf("failed to check if default group exists: %w", err)
	}
	if !defaultGroupExists {
		if err := m.createDefaultGroup(ctx); err != nil {
			return fmt.Errorf("failed to create default group: %w", err)
		}
	}

	// Migrate workloads to default group if they don't have a group assigned
	migratedCount, err := m.migrateWorkloadsToDefaultGroup(ctx)
	if err != nil {
		return fmt.Errorf("failed to migrate workloads: %w", err)
	}

	if migratedCount > 0 {
		fmt.Printf("\nSuccessfully migrated %d workloads to default group '%s'\n", migratedCount, groups.DefaultGroupName)
	}

	// Migrate client configurations from global config to default group
	if err := m.migrateClientConfigs(ctx); err != nil {
		return fmt.Errorf("failed to migrate client configurations: %w", err)
	}

	// Mark default group migration as completed
	err = config.UpdateConfig(func(c *config.Config) {
		c.DefaultGroupMigration = true
	})
	if err != nil {
		return fmt.Errorf("failed to update config during migration: %w", err)
	}

	return nil
}

// initManagers initializes the required managers
func (m *DefaultGroupMigrator) initManagers(ctx context.Context) error {
	var err error

	m.groupManager, err = groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	m.workloadsManager, err = workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workloads manager: %w", err)
	}

	return nil
}

// createDefaultGroup creates the default group if it doesn't exist
func (m *DefaultGroupMigrator) createDefaultGroup(ctx context.Context) error {
	logger.Infof("Creating default group '%s'", groups.DefaultGroupName)
	if err := m.groupManager.Create(ctx, groups.DefaultGroupName); err != nil {
		return fmt.Errorf("failed to create default group: %w", err)
	}
	return nil
}

// migrateWorkloadsToDefaultGroup migrates all workloads without a group to the default group
func (m *DefaultGroupMigrator) migrateWorkloadsToDefaultGroup(ctx context.Context) (int, error) {
	// Get all workloads that have no group assigned
	workloadsWithoutGroup, err := m.workloadsManager.ListWorkloadsInGroup(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("failed to list workloads without group: %w", err)
	}

	migratedCount := 0
	for _, workloadName := range workloadsWithoutGroup {
		// Move workload to default group
		if err := m.workloadsManager.MoveToGroup(ctx, []string{workloadName}, "", groups.DefaultGroup); err != nil {
			logger.Warnf("Failed to migrate workload %s to default group: %v", workloadName, err)
			continue
		}
		migratedCount++
	}

	return migratedCount, nil
}

// migrateClientConfigs migrates client configurations from global config to default group
func (m *DefaultGroupMigrator) migrateClientConfigs(ctx context.Context) error {
	appConfig := config.GetConfig()

	// If there are no registered clients, nothing to migrate
	if len(appConfig.Clients.RegisteredClients) == 0 {
		logger.Debugf("No client configurations to migrate")
		return nil
	}

	// Get the default group
	defaultGroup, err := m.groupManager.Get(ctx, groups.DefaultGroupName)
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
			if err := m.groupManager.RegisterClients(ctx, []string{groups.DefaultGroupName}, []string{clientName}); err != nil {
				logger.Warnf("Failed to register client %s to default group: %v", clientName, err)
				continue
			}
			migratedCount++
		}
	}

	if migratedCount > 0 {
		fmt.Printf("Successfully migrated %d client configurations to default group '%s'\n", migratedCount, groups.DefaultGroupName)

		// Clear the global client configurations after successful migration
		err = config.UpdateConfig(func(c *config.Config) {
			c.Clients.RegisteredClients = []string{}
		})
		if err != nil {
			logger.Warnf("Failed to clear global client configurations after migration: %v", err)
		} else {
			logger.Debugf("Cleared global client configurations")
		}
	} else {
		logger.Debugf("No client configurations needed migration")
	}

	return nil
}

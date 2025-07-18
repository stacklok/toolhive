package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

// migrationOnce ensures the migration only runs once
var migrationOnce sync.Once

// CheckAndPerformAutoDiscoveryMigration checks if auto-discovery migration is needed and performs it
// This is called once at application startup
func CheckAndPerformAutoDiscoveryMigration() {
	migrationOnce.Do(func() {
		// Create a temporary manager to access config
		ctx := context.Background()
		manager, err := NewManager(ctx)
		if err != nil {
			logger.Errorf("Error creating manager for migration: %v", err)
			return
		}

		appConfig, err := manager.GetConfig()
		if err != nil {
			logger.Errorf("Error getting config for migration: %v", err)
			return
		}

		// Check if auto-discovery flag is set to true, use of deprecated object is expected here
		if appConfig.Clients.AutoDiscovery {
			performAutoDiscoveryMigration(manager)
		}
	})
}

// performAutoDiscoveryMigration discovers and registers all installed clients
func performAutoDiscoveryMigration(manager Manager) {
	fmt.Println("Migrating from deprecated auto-discovery to manual client registration...")
	fmt.Println()

	// Get current client statuses to determine what to register
	clientStatuses, err := GetClientStatus()
	if err != nil {
		logger.Errorf("Error discovering clients during migration: %v", err)
		return
	}

	// Get current config to see what's already registered
	appConfig, err := manager.GetConfig()
	if err != nil {
		logger.Errorf("Error getting config during migration: %v", err)
		return
	}

	var clientsToRegister []string
	var alreadyRegistered = appConfig.Clients.RegisteredClients

	// Find installed clients that aren't registered yet
	for _, status := range clientStatuses {
		if status.Installed && !status.Registered {
			clientsToRegister = append(clientsToRegister, string(status.ClientType))
			fmt.Println("Registering client", string(status.ClientType))
		}
	}

	// Update config with new clients and remove auto-discovery flag
	appConfig.Clients.AutoDiscovery = false
	for _, clientName := range clientsToRegister {
		// Double-check if not already registered (safety check)
		found := false
		for _, registered := range appConfig.Clients.RegisteredClients {
			if registered == clientName {
				found = true
				break
			}
		}

		if !found {
			appConfig.Clients.RegisteredClients = append(appConfig.Clients.RegisteredClients, clientName)
		}
	}

	// Save the updated config
	if err := manager.SetConfig(appConfig); err != nil {
		logger.Errorf("Error updating config during migration: %v", err)
		return
	}

	// Print success messages for newly registered clients
	for _, clientName := range clientsToRegister {
		fmt.Printf("  ✓ Automatically registered client: %s\n", clientName)
	}

	fmt.Println()
	fmt.Println("NOTICE: Auto-discovery of MCP clients has been deprecated and is no longer supported.")
	fmt.Println("Your existing clients have been automatically migrated to the new manual registration system.")
	fmt.Println()
	fmt.Println("Going forward, use 'thv client setup' to discover and register new MCP clients.")
	fmt.Println("This provides better control and security for your client configurations.")
	fmt.Println()

	// Show all registered clients (both newly registered and previously registered)
	allRegisteredClients := append(alreadyRegistered, clientsToRegister...)
	if len(allRegisteredClients) > 0 {
		fmt.Println("Registered clients:")
		for _, clientName := range allRegisteredClients {
			fmt.Printf("  - %s\n", clientName)
		}
		fmt.Println()
	}
}

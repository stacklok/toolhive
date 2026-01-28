// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

// migrationOnce ensures the migration only runs once
var migrationOnce sync.Once

// CheckAndPerformAutoDiscoveryMigration checks if auto-discovery migration is needed and performs it
// This is called once at application startup
func CheckAndPerformAutoDiscoveryMigration() {
	migrationOnce.Do(func() {
		cfgprv := config.NewDefaultProvider()
		appConfig := cfgprv.GetConfig()

		// Check if auto-discovery flag is set to true, use of deprecated object is expected here
		if appConfig.Clients.AutoDiscovery {
			performAutoDiscoveryMigration()
		}
	})
}

// performAutoDiscoveryMigration discovers and registers all installed clients
func performAutoDiscoveryMigration() {
	fmt.Println("Migrating from deprecated auto-discovery to manual client registration...")
	fmt.Println()

	// Get current client statuses to determine what to register
	manager, err := NewClientManager()
	if err != nil {
		logger.Errorf("Error creating client manager during migration: %v", err)
		return
	}
	clientStatuses, err := manager.GetClientStatus(context.Background())
	if err != nil {
		logger.Errorf("Error discovering clients during migration: %v", err)
		return
	}

	var clientsToRegister []string

	// Find installed clients that aren't registered yet
	for _, status := range clientStatuses {
		if status.Installed && !status.Registered {
			clientsToRegister = append(clientsToRegister, string(status.ClientType))
			logger.Debugf("Registering client %s", string(status.ClientType))
		}
	}

	// Register new clients and remove the auto-discovery flag
	err = config.UpdateConfig(func(c *config.Config) {
		for _, clientName := range clientsToRegister {
			// Double-check if not already registered (safety check)
			found := false
			for _, registered := range c.Clients.RegisteredClients {
				if registered == clientName {
					found = true
					break
				}
			}

			if !found {
				c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, clientName)
			}
		}

		// Remove the auto-discovery flag during the same config update
		c.Clients.AutoDiscovery = false
	})

	if err != nil {
		logger.Errorf("Error updating config during migration: %v", err)
		return
	}

	fmt.Println()
	fmt.Println("NOTICE: Auto-discovery of MCP clients has been deprecated and is no longer supported.")
	fmt.Println("Your existing clients have been automatically migrated to the new manual registration system.")
	fmt.Println()
	fmt.Println("Going forward, use 'thv client setup' to discover and register new MCP clients.")
	fmt.Println("This provides better control and security for your client configurations.")
	fmt.Println()
}

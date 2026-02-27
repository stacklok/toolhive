// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	groupAddNames []string
	groupRmNames  []string
)

var clientCmd = &cobra.Command{
	Use:   "client",
	Short: "Manage MCP clients",
	Long:  "The client command provides subcommands to manage MCP client integrations.",
}

var clientStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all supported MCP clients",
	Long:  "Display the installation and registration status of all supported MCP clients in a table format.",
	RunE:  clientStatusCmdFunc,
}

var clientSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactively setup and register installed clients",
	Long:  `Presents a list of installed but unregistered clients for interactive selection and registration.`,
	RunE:  clientSetupCmdFunc,
}

var clientRegisterCmd = &cobra.Command{
	Use:   "register [client]",
	Short: "Register a client for MCP server configuration",
	Long: fmt.Sprintf(`Register a client for MCP server configuration.

Valid clients:
%s`, client.GetClientListFormatted()),
	Args: cobra.ExactArgs(1),
	RunE: clientRegisterCmdFunc,
}

var clientRemoveCmd = &cobra.Command{
	Use:   "remove [client]",
	Short: "Remove a client from MCP server configuration",
	Long: fmt.Sprintf(`Remove a client from MCP server configuration.

Valid clients:
%s`, client.GetClientListFormatted()),
	Args: cobra.ExactArgs(1),
	RunE: clientRemoveCmdFunc,
}

var clientListRegisteredCmd = &cobra.Command{
	Use:   "list-registered",
	Short: "List all registered MCP clients",
	Long:  "List all clients that are registered for MCP server configuration.",
	RunE:  listRegisteredClientsCmdFunc,
}

func init() {
	rootCmd.AddCommand(clientCmd)

	clientCmd.AddCommand(clientStatusCmd)
	clientCmd.AddCommand(clientSetupCmd)
	clientCmd.AddCommand(clientRegisterCmd)
	clientCmd.AddCommand(clientRemoveCmd)
	clientCmd.AddCommand(clientListRegisteredCmd)

	clientRegisterCmd.Flags().StringSliceVar(
		&groupAddNames, "group", []string{groups.DefaultGroup}, "Only register workloads from specified groups")
	clientRemoveCmd.Flags().StringSliceVar(
		&groupRmNames, "group", []string{}, "Remove client from specified groups (if not set, removes all workloads from the client)")
}

func clientStatusCmdFunc(cmd *cobra.Command, _ []string) error {
	clientStatuses, err := client.GetClientStatus(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to get client status: %w", err)
	}
	return ui.RenderClientStatusTable(clientStatuses)
}

func clientSetupCmdFunc(cmd *cobra.Command, _ []string) error {
	clientStatuses, err := client.GetClientStatus(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to get client status: %w", err)
	}
	availableClients := getAvailableClients(clientStatuses)
	if len(availableClients) == 0 {
		fmt.Println("No new clients found.")
		return nil
	}

	// Sort clients alphabetically by ClientType
	sort.Slice(availableClients, func(i, j int) bool {
		return availableClients[i].ClientType < availableClients[j].ClientType
	})
	// Get available groups for the UI
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	availableGroups, err := groupManager.List(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to list groups: %w", err)
	}

	selectedClients, selectedGroups, confirmed, err := ui.RunClientSetup(availableClients, availableGroups)
	if err != nil {
		if errors.Is(err, ui.ErrAllClientsRegistered) {
			fmt.Println("All installed clients are already registered for the selected groups.")
			return nil
		}
		return fmt.Errorf("error running interactive setup: %w", err)
	}
	if !confirmed {
		fmt.Println("Setup cancelled. No clients registered.")
		return nil
	}
	if len(selectedClients) == 0 {
		fmt.Println("No clients selected for registration.")
		return nil
	}
	if len(selectedGroups) == 0 && len(availableGroups) != 0 {
		fmt.Println("No groups selected for registration. Please select at least one group.")
		return nil
	}
	return registerSelectedClients(cmd, selectedClients, selectedGroups)
}

// Helper to get available (installed) clients
func getAvailableClients(statuses []client.ClientAppStatus) []client.ClientAppStatus {
	var available []client.ClientAppStatus
	for _, s := range statuses {
		if s.Installed {
			available = append(available, s)
		}
	}
	return available
}

// Helper to register selected clients
func registerSelectedClients(cmd *cobra.Command, clientsToRegister []client.ClientAppStatus, selectedGroups []string) error {
	clients := make([]client.Client, len(clientsToRegister))
	for i, cli := range clientsToRegister {
		clients[i] = client.Client{Name: cli.ClientType}
	}

	return performClientRegistration(cmd.Context(), clients, selectedGroups)
}

func clientRegisterCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	if !client.IsValidClient(clientType) {
		return fmt.Errorf("invalid client type: %s (valid types: %s)", clientType, client.GetClientListCSV())
	}

	return performClientRegistration(cmd.Context(), []client.Client{{Name: client.ClientApp(clientType)}}, groupAddNames)
}

func clientRemoveCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	if !client.IsValidClient(clientType) {
		return fmt.Errorf("invalid client type: %s (valid types: %s)", clientType, client.GetClientListCSV())
	}

	return performClientRemoval(cmd.Context(), client.Client{Name: client.ClientApp(clientType)}, groupRmNames)
}

func listRegisteredClientsCmdFunc(cmd *cobra.Command, _ []string) error {
	clientManager, err := client.NewManager(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	registeredClients, err := clientManager.ListClients(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to list registered clients: %w", err)
	}

	// Convert to UI format
	var uiClients []ui.RegisteredClient
	for _, regClient := range registeredClients {
		uiClient := ui.RegisteredClient{
			Name:   string(regClient.Name),
			Groups: regClient.Groups,
		}
		uiClients = append(uiClients, uiClient)
	}

	// Determine if we have groups by checking if any client has groups
	hasGroups := false
	for _, regClient := range registeredClients {
		if len(regClient.Groups) > 0 {
			hasGroups = true
			break
		}
	}

	return ui.RenderRegisteredClientsTable(uiClients, hasGroups)
}

func performClientRegistration(ctx context.Context, clients []client.Client, groupNames []string) error {
	clientManager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	runningWorkloads, err := workloadManager.ListWorkloads(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to list running workloads: %w", err)
	}

	if len(groupNames) > 0 {
		return registerClientsWithGroups(ctx, clients, groupNames, clientManager, runningWorkloads)
	}

	// We should never reach here once groups are enabled
	return registerClientsGlobally(clients, clientManager, runningWorkloads)
}

func registerClientsWithGroups(
	ctx context.Context,
	clients []client.Client,
	groupNames []string,
	clientManager client.Manager,
	runningWorkloads []core.Workload,
) error {
	slog.Debug(fmt.Sprintf("Filtering workloads to groups: %v", groupNames))

	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	clientNames := make([]string, len(clients))
	for i, clientToRegister := range clients {
		clientNames[i] = string(clientToRegister.Name)
	}

	// Register the clients in the groups
	err = groupManager.RegisterClients(ctx, groupNames, clientNames)
	if err != nil {
		return fmt.Errorf("failed to register clients with groups: %w", err)
	}

	filteredWorkloads, err := workloads.FilterByGroups(runningWorkloads, groupNames)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by groups: %w", err)
	}

	// Add the workloads to the client's configuration file
	err = clientManager.RegisterClients(clients, filteredWorkloads)
	if err != nil {
		return fmt.Errorf("failed to register clients: %w", err)
	}

	return nil
}

func registerClientsGlobally(
	clients []client.Client,
	clientManager client.Manager,
	runningWorkloads []core.Workload,
) error {
	for _, clientToRegister := range clients {
		// Update the global config to register the client
		err := config.UpdateConfig(func(c *config.Config) {
			for _, registeredClient := range c.Clients.RegisteredClients {
				if registeredClient == string(clientToRegister.Name) {
					slog.Debug(fmt.Sprintf("Client %s is already registered, skipping...", clientToRegister.Name))
					return
				}
			}

			c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, string(clientToRegister.Name))
		})
		if err != nil {
			return fmt.Errorf("failed to update configuration for client %s: %w", clientToRegister.Name, err)
		}

		slog.Debug(fmt.Sprintf("Successfully registered client: %s", clientToRegister.Name))
	}

	// Add the workloads to the client's configuration file
	err := clientManager.RegisterClients(clients, runningWorkloads)
	if err != nil {
		return fmt.Errorf("failed to register clients: %w", err)
	}

	return nil
}

func performClientRemoval(ctx context.Context, clientToRemove client.Client, groupNames []string) error {
	clientManager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	runningWorkloads, err := workloadManager.ListWorkloads(ctx, false)
	if err != nil {
		return fmt.Errorf("failed to list running workloads: %w", err)
	}

	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	if len(groupNames) > 0 {
		return removeClientFromGroups(ctx, clientToRemove, groupNames, runningWorkloads, groupManager, clientManager)
	}

	return removeClientGlobally(ctx, clientToRemove, runningWorkloads, groupManager, clientManager)
}

func removeClientFromGroups(
	ctx context.Context,
	clientToRemove client.Client,
	groupNames []string,
	runningWorkloads []core.Workload,
	groupManager groups.Manager,
	clientManager client.Manager,
) error {
	slog.Debug(fmt.Sprintf("Filtering workloads to groups: %v", groupNames))

	// Remove client from specific groups only
	filteredWorkloads, err := workloads.FilterByGroups(runningWorkloads, groupNames)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by groups: %w", err)
	}

	// Remove the workloads from the client's configuration file
	err = clientManager.UnregisterClients(ctx, []client.Client{clientToRemove}, filteredWorkloads)
	if err != nil {
		return fmt.Errorf("failed to unregister client: %w", err)
	}

	// Remove the client from the groups
	err = groupManager.UnregisterClients(ctx, groupNames, []string{string(clientToRemove.Name)})
	if err != nil {
		return fmt.Errorf("failed to unregister client from groups: %w", err)
	}

	slog.Debug(fmt.Sprintf("Successfully removed client %s from groups: %v", clientToRemove.Name, groupNames))

	return nil
}

func removeClientGlobally(
	ctx context.Context,
	clientToRemove client.Client,
	runningWorkloads []core.Workload,
	groupManager groups.Manager,
	clientManager client.Manager,
) error {
	// Remove the workloads from the client's configuration file
	err := clientManager.UnregisterClients(ctx, []client.Client{clientToRemove}, runningWorkloads)
	if err != nil {
		return fmt.Errorf("failed to unregister client: %w", err)
	}

	allGroups, err := groupManager.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list groups: %w", err)
	}

	if len(allGroups) > 0 {
		// Remove client from all groups first
		allGroupNames := make([]string, len(allGroups))
		for i, group := range allGroups {
			allGroupNames[i] = group.Name
		}

		err = groupManager.UnregisterClients(ctx, allGroupNames, []string{string(clientToRemove.Name)})
		if err != nil {
			return fmt.Errorf("failed to unregister client from groups: %w", err)
		}
	}

	// Remove client from global registered clients list
	err = config.UpdateConfig(func(c *config.Config) {
		for i, registeredClient := range c.Clients.RegisteredClients {
			if registeredClient == string(clientToRemove.Name) {
				// Remove client from slice
				c.Clients.RegisteredClients = append(c.Clients.RegisteredClients[:i], c.Clients.RegisteredClients[i+1:]...)
				slog.Debug(fmt.Sprintf("Successfully unregistered client: %s", clientToRemove.Name))
				return
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration for client %s: %w", clientToRemove.Name, err)
	}

	return nil
}

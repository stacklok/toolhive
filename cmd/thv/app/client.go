package app

import (
	"context"
	"fmt"
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
	groupNames []string
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
	Long: `Register a client for MCP server configuration.

Valid clients:
  - amp-cli: Sourcegraph Amp CLI
  - amp-cursor: Sourcegraph Amp extension for Cursor
  - amp-vscode: Sourcegraph Amp extension for VS Code
  - amp-vscode-insider: Sourcegraph Amp extension for VS Code Insiders
  - amp-windsurf: Sourcegraph Amp extension for Windsurf
  - claude-code: Claude Code CLI
  - cline: Cline extension for VS Code
  - cursor: Cursor editor
  - roo-code: Roo Code extension for VS Code
  - vscode: Visual Studio Code
  - vscode-insider: Visual Studio Code Insiders edition
  - windsurf: Windsurf IDE
  - windsurf-jetbrains: Windsurf for JetBrains IDEs`,
	Args: cobra.ExactArgs(1),
	RunE: clientRegisterCmdFunc,
}

var clientRemoveCmd = &cobra.Command{
	Use:   "remove [client]",
	Short: "Remove a client from MCP server configuration",
	Long: `Remove a client from MCP server configuration.

Valid clients:
  - amp-cli: Sourcegraph Amp CLI
  - amp-cursor: Sourcegraph Amp extension for Cursor
  - amp-vscode: Sourcegraph Amp extension for VS Code
  - amp-vscode-insider: Sourcegraph Amp extension for VS Code Insiders
  - amp-windsurf: Sourcegraph Amp extension for Windsurf
  - claude-code: Claude Code CLI
  - cline: Cline extension for VS Code
  - cursor: Cursor editor
  - roo-code: Roo Code extension for VS Code
  - vscode: Visual Studio Code
  - vscode-insider: Visual Studio Code Insiders edition
  - windsurf: Windsurf IDE
  - windsurf-jetbrains: Windsurf for JetBrains IDEs`,
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

	// TODO: Re-enable when group functionality is complete
	//clientRegisterCmd.Flags().StringSliceVar(
	//	&groupNames, "group", []string{"default"}, "Only register workloads from specified groups")
}

func clientStatusCmdFunc(_ *cobra.Command, _ []string) error {
	clientStatuses, err := client.GetClientStatus()
	if err != nil {
		return fmt.Errorf("failed to get client status: %w", err)
	}
	return ui.RenderClientStatusTable(clientStatuses)
}

func clientSetupCmdFunc(cmd *cobra.Command, _ []string) error {
	clientStatuses, err := client.GetClientStatus()
	if err != nil {
		return fmt.Errorf("failed to get client status: %w", err)
	}
	availableClients := getAvailableClients(clientStatuses)
	if len(availableClients) == 0 {
		fmt.Println("No new clients found.")
		return nil
	}
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

// Helper to get available (installed but unregistered) clients
func getAvailableClients(statuses []client.MCPClientStatus) []client.MCPClientStatus {
	var available []client.MCPClientStatus
	for _, s := range statuses {
		if s.Installed && !s.Registered {
			available = append(available, s)
		}
	}
	return available
}

// Helper to register selected clients
func registerSelectedClients(cmd *cobra.Command, clientsToRegister []client.MCPClientStatus, selectedGroups []string) error {
	clients := make([]client.Client, len(clientsToRegister))
	for i, cli := range clientsToRegister {
		clients[i] = client.Client{Name: cli.ClientType}
	}

	return performClientRegistration(cmd.Context(), clients, selectedGroups)
}

func clientRegisterCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cline", "cursor", "claude-code", "vscode-insider", "vscode", "windsurf", "windsurf-jetbrains",
		"amp-cli", "amp-vscode", "amp-vscode-insider", "amp-cursor", "amp-windsurf":
		// Valid client type
	default:
		return fmt.Errorf(
			"invalid client type: %s (valid types: roo-code, cline, cursor, claude-code, vscode, vscode-insider, "+
				"windsurf, windsurf-jetbrains, amp-cli, amp-vscode, amp-vscode-insider, amp-cursor, amp-windsurf)",
			clientType)
	}

	return performClientRegistration(cmd.Context(), []client.Client{{Name: client.MCPClient(clientType)}}, groupNames)
}

func clientRemoveCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cline", "cursor", "claude-code", "vscode-insider", "vscode", "windsurf", "windsurf-jetbrains",
		"amp-cli", "amp-vscode", "amp-vscode-insider", "amp-cursor", "amp-windsurf":
		// Valid client type
	default:
		return fmt.Errorf(
			"invalid client type: %s (valid types: roo-code, cline, cursor, claude-code, vscode, vscode-insider, "+
				"windsurf, windsurf-jetbrains, amp-cli, amp-vscode, amp-vscode-insider, amp-cursor, amp-windsurf)",
			clientType)
	}

	ctx := cmd.Context()

	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	err = manager.UnregisterClients(ctx, []client.Client{
		{Name: client.MCPClient(clientType)},
	})
	if err != nil {
		return fmt.Errorf("failed to remove client %s: %w", clientType, err)
	}

	return nil
}

func listRegisteredClientsCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()
	if len(cfg.Clients.RegisteredClients) == 0 {
		fmt.Println("No clients are currently registered.")
		return nil
	}

	// Create a copy of the registered clients and sort it alphabetically
	registeredClients := make([]string, len(cfg.Clients.RegisteredClients))
	copy(registeredClients, cfg.Clients.RegisteredClients)
	sort.Strings(registeredClients)

	fmt.Println("Registered clients:")
	for _, clientName := range registeredClients {
		fmt.Printf("  - %s\n", clientName)
	}
	return nil
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
	fmt.Printf("Filtering workloads to groups: %v\n", groupNames)

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

	filteredWorkloads, err := workloads.FilterByGroups(ctx, runningWorkloads, groupNames)
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
					fmt.Printf("Client %s is already registered, skipping...\n", clientToRegister.Name)
					return
				}
			}

			c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, string(clientToRegister.Name))
		})
		if err != nil {
			return fmt.Errorf("failed to update configuration for client %s: %w", clientToRegister.Name, err)
		}

		fmt.Printf("Successfully registered client: %s\n", clientToRegister.Name)
	}

	// Add the workloads to the client's configuration file
	err := clientManager.RegisterClients(clients, runningWorkloads)
	if err != nil {
		return fmt.Errorf("failed to register clients: %w", err)
	}

	return nil
}

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/cmd/thv/app/ui"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
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

func init() {
	rootCmd.AddCommand(clientCmd)

	clientCmd.AddCommand(clientStatusCmd)
	clientCmd.AddCommand(clientSetupCmd)
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
		fmt.Println("All installed clients are already registered.")
		return nil
	}
	selected, confirmed, err := ui.RunClientSetup(availableClients)
	if err != nil {
		return fmt.Errorf("error running interactive setup: %w", err)
	}
	if !confirmed {
		fmt.Println("Setup cancelled. No clients registered.")
		return nil
	}
	if len(selected) == 0 {
		fmt.Println("No clients selected for registration.")
		return nil
	}
	return registerSelectedClients(cmd, selected)
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
func registerSelectedClients(cmd *cobra.Command, clientsToRegister []client.MCPClientStatus) error {
	err := config.UpdateConfig(func(c *config.Config) {
		registeredClientsMap := make(map[string]bool)
		for _, registeredClient := range c.Clients.RegisteredClients {
			registeredClientsMap[registeredClient] = true
		}
		for _, cli := range clientsToRegister {
			clientName := string(cli.ClientType)
			if _, ok := registeredClientsMap[clientName]; !ok {
				c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, clientName)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Registering selected clients...")
	for _, cli := range clientsToRegister {
		clientName := string(cli.ClientType)
		fmt.Printf("Successfully registered client: %s\n", clientName)
		if err := addRunningMCPsToClient(cmd.Context(), clientName); err != nil {
			fmt.Printf("Warning: Failed to add running MCPs to client %s: %v\n", clientName, err)
		}
	}
	return nil
}

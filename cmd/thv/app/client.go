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

var clientRegisterCmd = &cobra.Command{
	Use:   "register [client]",
	Short: "Register a client for MCP server configuration",
	Long: `Register a client for MCP server configuration.
Valid clients are:
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
Valid clients are:
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
	ctx := cmd.Context()

	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	clients := make([]client.Client, len(clientsToRegister))
	for i, cli := range clientsToRegister {
		clients[i] = client.Client{Name: cli.ClientType}
	}

	err = manager.RegisterClients(ctx, clients)
	if err != nil {
		return fmt.Errorf("failed to register clients: %w", err)
	}

	return nil
}

func clientRegisterCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cline", "cursor", "claude-code", "vscode-insider", "vscode", "windsurf", "windsurf-jetbrains":
		// Valid client type
	default:
		return fmt.Errorf(
			"invalid client type: %s (valid types: roo-code, cline, cursor, claude-code, vscode, "+
				"vscode-insider, windsurf, windsurf-jetbrains)",
			clientType)
	}

	ctx := cmd.Context()

	manager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	err = manager.RegisterClients(ctx, []client.Client{
		{Name: client.MCPClient(clientType)},
	})
	if err != nil {
		return fmt.Errorf("failed to register client %s: %w", clientType, err)
	}

	return nil
}

func clientRemoveCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cline", "cursor", "claude-code", "vscode-insider", "vscode", "windsurf", "windsurf-jetbrains":
		// Valid client type
	default:
		return fmt.Errorf(
			"invalid client type: %s (valid types: roo-code, cline, cursor, claude-code, vscode, "+
				"vscode-insider, windsurf, windsurf-jetbrains)",
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
	fmt.Println("Registered clients:")
	for _, clientName := range cfg.Clients.RegisteredClients {
		fmt.Printf("  - %s\n", clientName)
	}
	return nil
}

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/certs"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage application configuration",
	Long:  "The config command provides subcommands to manage application configuration settings.",
}

var listRegisteredClientsCmd = &cobra.Command{
	Use:   "list-registered-clients",
	Short: "List all registered MCP clients",
	Long:  "List all clients that are registered for MCP server configuration.",
	RunE:  listRegisteredClientsCmdFunc,
}

var registerClientCmd = &cobra.Command{
	Use:   "register-client [client]",
	Short: "Register a client for MCP server configuration",
	Long: `Register a client for MCP server configuration.
Valid clients are:
  - claude-code: Claude Code CLI
  - cline: Cline extension for VS Code
  - cursor: Cursor editor
  - roo-code: Roo Code extension for VS Code
  - vscode: Visual Studio Code
  - vscode-insider: Visual Studio Code Insiders edition`,
	Args: cobra.ExactArgs(1),
	RunE: registerClientCmdFunc,
}

var removeClientCmd = &cobra.Command{
	Use:   "remove-client [client]",
	Short: "Remove a client from MCP server configuration",
	Long: `Remove a client from MCP server configuration.
Valid clients are:
  - claude-code: Claude Code CLI
  - cline: Cline extension for VS Code
  - cursor: Cursor editor
  - roo-code: Roo Code extension for VS Code
  - vscode: Visual Studio Code
  - vscode-insider: Visual Studio Code Insiders edition`,
	Args: cobra.ExactArgs(1),
	RunE: removeClientCmdFunc,
}

var setCACertCmd = &cobra.Command{
	Use:   "set-ca-cert <path>",
	Short: "Set the default CA certificate for container builds",
	Long: `Set the default CA certificate file path that will be used for all container builds.
This is useful in corporate environments with TLS inspection where custom CA certificates are required.

Example:
  thv config set-ca-cert /path/to/corporate-ca.crt`,
	Args: cobra.ExactArgs(1),
	RunE: setCACertCmdFunc,
}

var getCACertCmd = &cobra.Command{
	Use:   "get-ca-cert",
	Short: "Get the currently configured CA certificate path",
	Long:  "Display the path to the CA certificate file that is currently configured for container builds.",
	RunE:  getCACertCmdFunc,
}

var unsetCACertCmd = &cobra.Command{
	Use:   "unset-ca-cert",
	Short: "Remove the configured CA certificate",
	Long:  "Remove the CA certificate configuration, reverting to default behavior without custom CA certificates.",
	RunE:  unsetCACertCmdFunc,
}

var setRegistryURLCmd = &cobra.Command{
	Use:   "set-registry-url <url>",
	Short: "Set the MCP server registry URL",
	Long: `Set the URL for the remote MCP server registry.
This allows you to use a custom registry instead of the built-in one.

Example:
  thv config set-registry-url https://example.com/registry.json`,
	Args: cobra.ExactArgs(1),
	RunE: setRegistryURLCmdFunc,
}

var getRegistryURLCmd = &cobra.Command{
	Use:   "get-registry-url",
	Short: "Get the currently configured registry URL",
	Long:  "Display the URL of the remote registry that is currently configured.",
	RunE:  getRegistryURLCmdFunc,
}

var unsetRegistryURLCmd = &cobra.Command{
	Use:   "unset-registry-url",
	Short: "Remove the configured registry URL",
	Long:  "Remove the registry URL configuration, reverting to the built-in registry.",
	RunE:  unsetRegistryURLCmdFunc,
}

var clientStatusCmd = &cobra.Command{
	Use:   "client-status",
	Short: "Show status of all supported MCP clients",
	Long:  "Display the installation and registration status of all supported MCP clients in a table format.",
	RunE:  clientStatusCmdFunc,
}

var clientSetupCmd = &cobra.Command{
	Use:   "client-setup",
	Short: "Interactively setup and register installed clients",
	Long:  `Presents a list of installed but unregistered clients for interactive selection and registration.`,
	RunE:  clientSetupCmdFunc,
}

func init() {
	// Add config command to root command
	rootCmd.AddCommand(configCmd)

	// Add subcommands to config command
	configCmd.AddCommand(registerClientCmd)
	configCmd.AddCommand(removeClientCmd)
	configCmd.AddCommand(listRegisteredClientsCmd)
	configCmd.AddCommand(setCACertCmd)
	configCmd.AddCommand(getCACertCmd)
	configCmd.AddCommand(unsetCACertCmd)
	configCmd.AddCommand(setRegistryURLCmd)
	configCmd.AddCommand(getRegistryURLCmd)
	configCmd.AddCommand(unsetRegistryURLCmd)
	configCmd.AddCommand(clientStatusCmd)
	configCmd.AddCommand(clientSetupCmd)
}

func registerClientCmdFunc(cmd *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cline", "cursor", "claude-code", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf(
			"invalid client type: %s (valid types: roo-code, cline, cursor, claude-code, vscode, vscode-insider)",
			clientType)
	}

	err := config.UpdateConfig(func(c *config.Config) {
		// Check if client is already registered and skip.
		for _, registeredClient := range c.Clients.RegisteredClients {
			if registeredClient == clientType {
				fmt.Printf("Client %s is already registered, skipping...\n", clientType)
				return
			}
		}

		// Add the client to the registered clients list
		c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, clientType)
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully registered client: %s\n", clientType)

	// Add currently running MCPs to the newly registered client
	if err := addRunningMCPsToClient(cmd.Context(), clientType); err != nil {
		fmt.Printf("Warning: Failed to add running MCPs to client: %v\n", err)
	}

	return nil
}

func removeClientCmdFunc(_ *cobra.Command, args []string) error {
	clientType := args[0]

	// Validate the client type
	switch clientType {
	case "roo-code", "cline", "cursor", "claude-code", "vscode-insider", "vscode":
		// Valid client type
	default:
		return fmt.Errorf(
			"invalid client type: %s (valid types: roo-code, cline, cursor, claude-code, vscode, vscode-insider)",
			clientType)
	}

	err := config.UpdateConfig(func(c *config.Config) {
		// Find and remove the client from the registered clients list
		found := false
		for i, registeredClient := range c.Clients.RegisteredClients {
			if registeredClient == clientType {
				// Remove the client by appending the slice before and after the index
				c.Clients.RegisteredClients = append(c.Clients.RegisteredClients[:i], c.Clients.RegisteredClients[i+1:]...)
				found = true
				break
			}
		}
		if found {
			fmt.Printf("Client %s removed from registered clients.\n", clientType)
		} else {
			fmt.Printf("Client %s not found in registered clients.\n", clientType)
		}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully removed client: %s\n", clientType)
	return nil
}

// addRunningMCPsToClient adds currently running MCP servers to the specified client's configuration
func addRunningMCPsToClient(ctx context.Context, clientName string) error {
	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// List workloads
	containers, err := runtime.ListWorkloads(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive and running
	var runningContainers []rt.ContainerInfo
	for _, c := range containers {
		if labels.IsToolHiveContainer(c.Labels) && c.State == "running" {
			runningContainers = append(runningContainers, c)
		}
	}

	if len(runningContainers) == 0 {
		// No running servers, nothing to do
		return nil
	}

	// Find the client configuration for the specified client
	clientConfigs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	// If no configs found, nothing to do
	if len(clientConfigs) == 0 {
		return nil
	}

	// For each running container, add it to the client configuration
	for _, c := range runningContainers {
		// Get container name from labels
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Get tool type from labels
		toolType := labels.GetToolType(c.Labels)

		// Only include containers with tool type "mcp"
		if toolType != "mcp" {
			continue
		}

		// Get port from labels
		port, err := labels.GetPort(c.Labels)
		if err != nil {
			continue // Skip if we can't get the port
		}

		transportType := labels.GetTransportType(c.Labels)

		// Generate URL for the MCP server
		url := client.GenerateMCPServerURL(transportType, transport.LocalhostIPv4, port, name)

		// Update each configuration file
		for _, clientConfig := range clientConfigs {
			// Update the MCP server configuration with locking
			if err := client.Upsert(clientConfig, name, url); err != nil {
				logger.Warnf("Warning: Failed to update MCP server configuration in %s: %v", clientConfig.Path, err)
				continue
			}

			fmt.Printf("Added MCP server %s to client %s\n", name, clientName)
		}
	}

	return nil
}

func setCACertCmdFunc(_ *cobra.Command, args []string) error {
	certPath := filepath.Clean(args[0])

	// Validate that the file exists and is readable
	if _, err := os.Stat(certPath); err != nil {
		return fmt.Errorf("CA certificate file not found or not accessible: %w", err)
	}

	// Read and validate the certificate
	certContent, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate file: %w", err)
	}

	// Validate the certificate format
	if err := certs.ValidateCACertificate(certContent); err != nil {
		return fmt.Errorf("invalid CA certificate: %w", err)
	}

	// Update the configuration
	err = config.UpdateConfig(func(c *config.Config) {
		c.CACertificatePath = certPath
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set CA certificate path: %s\n", certPath)
	return nil
}

func getCACertCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.CACertificatePath == "" {
		fmt.Println("No CA certificate is currently configured.")
		return nil
	}

	fmt.Printf("Current CA certificate path: %s\n", cfg.CACertificatePath)

	// Check if the file still exists
	if _, err := os.Stat(cfg.CACertificatePath); err != nil {
		fmt.Printf("Warning: The configured CA certificate file is not accessible: %v\n", err)
	}

	return nil
}

func unsetCACertCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.CACertificatePath == "" {
		fmt.Println("No CA certificate is currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.CACertificatePath = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed CA certificate configuration.")
	return nil
}

func setRegistryURLCmdFunc(_ *cobra.Command, args []string) error {
	registryURL := args[0]

	// Basic URL validation - check if it starts with http:// or https://
	if registryURL != "" && !strings.HasPrefix(registryURL, "http://") && !strings.HasPrefix(registryURL, "https://") {
		return fmt.Errorf("registry URL must start with http:// or https://")
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.RegistryUrl = registryURL
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully set registry URL: %s\n", registryURL)
	return nil
}

func getRegistryURLCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.RegistryUrl == "" {
		fmt.Println("No custom registry URL is currently configured. Using built-in registry.")
		return nil
	}

	fmt.Printf("Current registry URL: %s\n", cfg.RegistryUrl)
	return nil
}

func unsetRegistryURLCmdFunc(_ *cobra.Command, _ []string) error {
	cfg := config.GetConfig()

	if cfg.RegistryUrl == "" {
		fmt.Println("No custom registry URL is currently configured.")
		return nil
	}

	// Update the configuration
	err := config.UpdateConfig(func(c *config.Config) {
		c.RegistryUrl = ""
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Successfully removed registry URL configuration. Will use built-in registry.")
	return nil
}

func listRegisteredClientsCmdFunc(_ *cobra.Command, _ []string) error {
	// Get the current config
	cfg := config.GetConfig()

	// Check if there are any registered clients
	if len(cfg.Clients.RegisteredClients) == 0 {
		fmt.Println("No clients are currently registered.")
		return nil
	}

	// Print the list of registered clients
	fmt.Println("Registered clients:")
	for _, clientName := range cfg.Clients.RegisteredClients {
		fmt.Printf("  - %s\n", clientName)
	}

	return nil
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
		for _, clientToRegister := range clientsToRegister {
			clientName := string(clientToRegister.ClientType)
			if _, ok := registeredClientsMap[clientName]; !ok {
				c.Clients.RegisteredClients = append(c.Clients.RegisteredClients, clientName)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Println("Registering selected clients...")
	for _, clientToRegister := range clientsToRegister {
		clientName := string(clientToRegister.ClientType)
		fmt.Printf("Successfully registered client: %s\n", clientName)
		if err := addRunningMCPsToClient(cmd.Context(), clientName); err != nil {
			fmt.Printf("Warning: Failed to add running MCPs to client %s: %v\n", clientName, err)
		}
	}
	return nil
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

	initialModel := &setupModel{
		clients:  availableClients,
		selected: make(map[int]struct{}),
	}

	p := tea.NewProgram(initialModel)
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("error running interactive setup: %w", err)
	}

	model := finalModel.(*setupModel)
	if !model.confirmed {
		fmt.Println("Setup cancelled. No clients registered.")
		return nil
	}

	// Get selected clients
	var clientsToRegister []client.MCPClientStatus
	for i := range model.selected {
		clientsToRegister = append(clientsToRegister, availableClients[i])
	}

	if len(clientsToRegister) == 0 {
		fmt.Println("No clients selected for registration.")
		return nil
	}

	return registerSelectedClients(cmd, clientsToRegister)
}

var (
	docStyle          = lipgloss.NewStyle().Margin(1, 2)
	selectedItemStyle = lipgloss.NewStyle().Padding(0, 2).Foreground(lipgloss.Color("170"))
	itemStyle         = lipgloss.NewStyle().Padding(0, 2)
)

type setupModel struct {
	clients   []client.MCPClientStatus
	cursor    int
	selected  map[int]struct{}
	quitting  bool
	confirmed bool // true if user pressed enter, false if quit
}

func (*setupModel) Init() tea.Cmd {
	return nil
}

func (m *setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "ctrl+c", "q":
			m.confirmed = false
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.clients)-1 {
				m.cursor++
			}
		case "enter":
			m.confirmed = true
			m.quitting = true
			return m, tea.Quit
		case " ":
			if _, ok := m.selected[m.cursor]; ok {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
			}
		}
	}
	return m, nil
}

func (m *setupModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	b.WriteString("Select clients to register:\n\n")

	for i, cli := range m.clients {
		b.WriteString(m.renderClientRow(i, cli))
	}

	b.WriteString("\nUse ↑/↓ (or j/k) to move, 'space' to select, 'enter' to confirm, 'q' to quit.\n")
	return docStyle.Render(b.String())
}

// renderClientRow returns the formatted string for a single client row
func (m *setupModel) renderClientRow(i int, cli client.MCPClientStatus) string {
	// Cursor indicator
	cursor := "  "
	if m.cursor == i {
		cursor = "> "
	}

	// Checkbox indicator
	checked := " "
	if _, ok := m.selected[i]; ok {
		checked = "x"
	}

	row := fmt.Sprintf("%s[%s] %s", cursor, checked, cli.ClientType)

	// Apply style and add newline
	if m.cursor == i {
		return selectedItemStyle.Render(row) + "\n"
	}
	return itemStyle.Render(row) + "\n"
}

func clientStatusCmdFunc(_ *cobra.Command, _ []string) error {
	// Get client status for all supported clients
	clientStatuses, err := client.GetClientStatus()
	if err != nil {
		return fmt.Errorf("failed to get client status: %w", err)
	}

	if len(clientStatuses) == 0 {
		fmt.Println("No supported clients found.")
		return nil
	}

	// Create a table writer for output
	table := tablewriter.NewWriter(os.Stdout)
	table.Options(
		tablewriter.WithHeader([]string{"Client Type", "Installed", "Registered"}),
		tablewriter.WithRendition(
			tw.Rendition{
				Borders: tw.Border{
					Left:   tw.State(1),
					Top:    tw.State(1),
					Right:  tw.State(1),
					Bottom: tw.State(1),
				},
			},
		),
		tablewriter.WithAlignment(tw.MakeAlign(3, tw.AlignLeft)),
	)

	// Add rows to the table
	for _, status := range clientStatuses {
		installed := "❌ No"
		if status.Installed {
			installed = "✅ Yes"
		}

		registered := "❌ No"
		if status.Registered {
			registered = "✅ Yes"
		}

		if err := table.Append([]string{
			string(status.ClientType),
			installed,
			registered,
		}); err != nil {
			return fmt.Errorf("failed to append row: %w", err)
		}
	}

	if err := table.Render(); err != nil {
		return fmt.Errorf("failed to render table: %w", err)
	}

	return nil
}

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/ssecommon"
	"github.com/stacklok/toolhive/pkg/transport/streamable"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	mcpServerURL string
	mcpFormat    string
	mcpTimeout   time.Duration
	mcpTransport string
)

func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Interact with MCP servers for debugging",
		Long:  `The mcp command provides subcommands to interact with MCP (Model Context Protocol) servers for debugging purposes.`,
	}

	// Add serve subcommand
	cmd.AddCommand(newMCPServeCommand())

	// Create list command
	listCmd := &cobra.Command{
		Use:   "list [tools|resources|prompts]",
		Short: "List MCP server capabilities",
		Long:  `List tools, resources, and prompts available from an MCP server. Use subcommands to list specific types.`,
		RunE:  mcpListCmdFunc,
	}

	// Create specific list subcommands
	toolsCmd := &cobra.Command{
		Use:   "tools",
		Short: "List available tools from MCP server",
		Long:  `List all tools available from the specified MCP server.`,
		RunE:  mcpListToolsCmdFunc,
	}

	resourcesCmd := &cobra.Command{
		Use:   "resources",
		Short: "List available resources from MCP server",
		Long:  `List all resources available from the specified MCP server.`,
		RunE:  mcpListResourcesCmdFunc,
	}

	promptsCmd := &cobra.Command{
		Use:   "prompts",
		Short: "List available prompts from MCP server",
		Long:  `List all prompts available from the specified MCP server.`,
		RunE:  mcpListPromptsCmdFunc,
	}

	// Add flags to all MCP commands
	addMCPFlags(listCmd)
	addMCPFlags(toolsCmd)
	addMCPFlags(resourcesCmd)
	addMCPFlags(promptsCmd)

	// Add specific list subcommands to list command
	listCmd.AddCommand(toolsCmd)
	listCmd.AddCommand(resourcesCmd)
	listCmd.AddCommand(promptsCmd)

	// Add list subcommand to mcp
	cmd.AddCommand(listCmd)

	return cmd
}

func addMCPFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&mcpServerURL, "server", "", "MCP server URL or name from ToolHive registry (required)")
	cmd.Flags().StringVar(&mcpFormat, "format", FormatText, "Output format (json or text)")
	cmd.Flags().DurationVar(&mcpTimeout, "timeout", 30*time.Second, "Connection timeout")
	cmd.Flags().StringVar(&mcpTransport, "transport", "auto", "Transport type (auto, sse, streamable-http)")
	_ = cmd.MarkFlagRequired("server")
}

// mcpListCmdFunc lists all capabilities (tools, resources, prompts)
func mcpListCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), mcpTimeout)
	defer cancel()

	// Resolve server URL if it's a name
	serverURL, err := resolveServerURL(ctx, mcpServerURL)
	if err != nil {
		return err
	}

	mcpClient, err := createMCPClient(serverURL)
	if err != nil {
		return err
	}
	defer mcpClient.Close()

	if err := initializeMCPClient(ctx, mcpClient); err != nil {
		return err
	}

	// Collect all data
	data := make(map[string]interface{})

	// List tools
	if tools, err := mcpClient.ListTools(ctx, &mcp.ListToolsParams{}); err != nil {
		logger.Warnf("Failed to list tools: %v", err)
		data["tools"] = []mcp.Tool{}
	} else {
		data["tools"] = tools.Tools
	}

	// List resources
	if resources, err := mcpClient.ListResources(ctx, &mcp.ListResourcesParams{}); err != nil {
		logger.Warnf("Failed to list resources: %v", err)
		data["resources"] = []mcp.Resource{}
	} else {
		data["resources"] = resources.Resources
	}

	// List prompts
	if prompts, err := mcpClient.ListPrompts(ctx, &mcp.ListPromptsParams{}); err != nil {
		logger.Warnf("Failed to list prompts: %v", err)
		data["prompts"] = []mcp.Prompt{}
	} else {
		data["prompts"] = prompts.Prompts
	}

	return outputMCPData(data, mcpFormat)
}

// mcpListToolsCmdFunc lists only tools
func mcpListToolsCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), mcpTimeout)
	defer cancel()

	// Resolve server URL if it's a name
	serverURL, err := resolveServerURL(ctx, mcpServerURL)
	if err != nil {
		return err
	}

	mcpClient, err := createMCPClient(serverURL)
	if err != nil {
		return err
	}
	defer mcpClient.Close()

	if err := initializeMCPClient(ctx, mcpClient); err != nil {
		return err
	}

	result, err := mcpClient.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	return outputMCPData(map[string]interface{}{"tools": result.Tools}, mcpFormat)
}

// mcpListResourcesCmdFunc lists only resources
func mcpListResourcesCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), mcpTimeout)
	defer cancel()

	// Resolve server URL if it's a name
	serverURL, err := resolveServerURL(ctx, mcpServerURL)
	if err != nil {
		return err
	}

	mcpClient, err := createMCPClient(serverURL)
	if err != nil {
		return err
	}
	defer mcpClient.Close()

	if err := initializeMCPClient(ctx, mcpClient); err != nil {
		return err
	}

	result, err := mcpClient.ListResources(ctx, &mcp.ListResourcesParams{})
	if err != nil {
		return fmt.Errorf("failed to list resources: %w", err)
	}

	return outputMCPData(map[string]interface{}{"resources": result.Resources}, mcpFormat)
}

// mcpListPromptsCmdFunc lists only prompts
func mcpListPromptsCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), mcpTimeout)
	defer cancel()

	// Resolve server URL if it's a name
	serverURL, err := resolveServerURL(ctx, mcpServerURL)
	if err != nil {
		return err
	}

	mcpClient, err := createMCPClient(serverURL)
	if err != nil {
		return err
	}
	defer mcpClient.Close()

	if err := initializeMCPClient(ctx, mcpClient); err != nil {
		return err
	}

	result, err := mcpClient.ListPrompts(ctx, &mcp.ListPromptsParams{})
	if err != nil {
		return fmt.Errorf("failed to list prompts: %w", err)
	}

	return outputMCPData(map[string]interface{}{"prompts": result.Prompts}, mcpFormat)
}

// resolveServerURL resolves a server name to a URL or returns the URL if it's already a URL
func resolveServerURL(ctx context.Context, serverInput string) (string, error) {
	// Check if it's already a URL
	if strings.HasPrefix(serverInput, "http://") || strings.HasPrefix(serverInput, "https://") {
		return serverInput, nil
	}

	// Try to get the workload by name
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to create workload manager: %w", err)
	}

	workload, err := manager.GetWorkload(ctx, serverInput)
	if err != nil {
		return "", fmt.Errorf(
			"server '%s' not found in running workloads. "+
				"Please ensure the server is running or provide a valid URL", serverInput)
	}

	// Check if the workload is running
	if workload.Status != "running" {
		return "", fmt.Errorf("server '%s' is not running (status: %s). "+
			"Please start it first using 'thv run %s'", serverInput, workload.Status, serverInput)
	}

	return workload.URL, nil
}

// createMCPClient creates an MCP client based on the server URL and transport type
func createMCPClient(serverURL string) (*mcp.ClientSession, error) {
	transportType := determineTransportType(serverURL, mcpTransport)

	// Create the MCP client
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "thv-mcp-cli",
			Version: versions.Version,
		},
		&mcp.ClientOptions{},
	)

	var transport mcp.Transport

	switch transportType {
	case types.TransportTypeSSE:
		transport = &mcp.SSEClientTransport{
			Endpoint: serverURL,
		}
	case types.TransportTypeStreamableHTTP:
		transport = &mcp.StreamableClientTransport{
			Endpoint:   serverURL,
			MaxRetries: 5,
		}
	case types.TransportTypeStdio:
		return nil, fmt.Errorf("stdio transport is not supported for MCP client connections")
	case types.TransportTypeInspector:
		return nil, fmt.Errorf("inspector transport is not supported for MCP client connections")
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}

	// Connect using the transport
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	return session, nil
}

// determineTransportType determines the transport type based on URL path and user preference
func determineTransportType(serverURL, transportFlag string) types.TransportType {
	// If user explicitly specified a transport type, use it (unless it's "auto")
	if transportFlag != "auto" {
		switch transportFlag {
		case string(types.TransportTypeSSE):
			return types.TransportTypeSSE
		case string(types.TransportTypeStreamableHTTP):
			return types.TransportTypeStreamableHTTP
		}
	}

	// Auto-detect based on URL path
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		// If we can't parse the URL, default to SSE for backward compatibility
		logger.Warnf("Failed to parse server URL %s, defaulting to SSE transport: %v", serverURL, err)
		return types.TransportTypeSSE
	}

	path := parsedURL.Path

	// Check for streamable HTTP endpoint (/mcp)
	if strings.HasSuffix(path, "/"+streamable.HTTPStreamableHTTPEndpoint) ||
		strings.HasSuffix(path, streamable.HTTPStreamableHTTPEndpoint) {
		return types.TransportTypeStreamableHTTP
	}

	// Check for SSE endpoint (/sse)
	if strings.HasSuffix(path, ssecommon.HTTPSSEEndpoint) {
		return types.TransportTypeSSE
	}

	// Default to SSE for backward compatibility
	return types.TransportTypeSSE
}

// initializeMCPClient initializes the MCP client connection
func initializeMCPClient(ctx context.Context, mcpClient *mcp.ClientSession) error {
	// Initialization happens during Connect, just verify we're connected
	if mcpClient == nil {
		return fmt.Errorf("client session not connected")
	}
	result := mcpClient.InitializeResult()
	if result == nil {
		return fmt.Errorf("client session not initialized")
	}
	return nil
}

// outputMCPData outputs the MCP data in the specified format
func outputMCPData(data map[string]interface{}, format string) error {
	switch format {
	case FormatJSON:
		return outputMCPJSON(data)
	default:
		return outputMCPText(data)
	}
}

// outputMCPJSON outputs MCP data in JSON format
func outputMCPJSON(data map[string]interface{}) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(jsonData))
	return nil
}

// outputMCPText outputs MCP data in text format
func outputMCPText(data map[string]interface{}) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)

	hasData := outputMCPTools(w, data) ||
		outputMCPResources(w, data) ||
		outputMCPPrompts(w, data)

	if !hasData {
		fmt.Println("No tools, resources, or prompts found")
		return nil
	}

	return w.Flush()
}

// outputMCPTools outputs tools data to the tabwriter
func outputMCPTools(w *tabwriter.Writer, data map[string]interface{}) bool {
	tools, ok := data["tools"].([]mcp.Tool)
	if !ok || len(tools) == 0 {
		return false
	}

	fmt.Fprintln(w, "TOOLS:")
	fmt.Fprintln(w, "NAME\tDESCRIPTION")
	for _, tool := range tools {
		fmt.Fprintf(w, "%s\t%s\n", tool.Name, tool.Description)
	}
	fmt.Fprintln(w, "")
	return true
}

// outputMCPResources outputs resources data to the tabwriter
func outputMCPResources(w *tabwriter.Writer, data map[string]interface{}) bool {
	resources, ok := data["resources"].([]mcp.Resource)
	if !ok || len(resources) == 0 {
		return false
	}

	fmt.Fprintln(w, "RESOURCES:")
	fmt.Fprintln(w, "NAME\tURI\tDESCRIPTION\tMIME_TYPE")
	for _, resource := range resources {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			resource.Name, resource.URI, resource.Description, resource.MIMEType)
	}
	fmt.Fprintln(w, "")
	return true
}

// outputMCPPrompts outputs prompts data to the tabwriter
func outputMCPPrompts(w *tabwriter.Writer, data map[string]interface{}) bool {
	prompts, ok := data["prompts"].([]mcp.Prompt)
	if !ok || len(prompts) == 0 {
		return false
	}

	fmt.Fprintln(w, "PROMPTS:")
	fmt.Fprintln(w, "NAME\tDESCRIPTION\tARGUMENTS")
	for _, prompt := range prompts {
		argStr := formatPromptArguments(prompt.Arguments)
		fmt.Fprintf(w, "%s\t%s\t%s\n", prompt.Name, prompt.Description, argStr)
	}
	fmt.Fprintln(w, "")
	return true
}

// formatPromptArguments formats the prompt arguments for display
func formatPromptArguments(arguments []*mcp.PromptArgument) string {
	argCount := len(arguments)
	if argCount == 0 {
		return "0"
	}

	argNames := make([]string, len(arguments))
	for i, arg := range arguments {
		argNames[i] = arg.Name
	}
	return fmt.Sprintf("%d (%v)", argCount, argNames)
}

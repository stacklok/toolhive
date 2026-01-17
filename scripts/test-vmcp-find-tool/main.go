//go:build ignore
// +build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <query> [server_url]")
		fmt.Println("Example: go run main.go 'read pull requests from GitHub'")
		fmt.Println("Default server URL: http://localhost:4483/mcp")
		os.Exit(1)
	}

	query := os.Args[1]
	serverURL := "http://localhost:4483/mcp"
	if len(os.Args) >= 3 {
		serverURL = os.Args[2]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Printf("🔍 Testing optim.find_tool via vmcp server\n")
	fmt.Printf("   Server: %s\n", serverURL)
	fmt.Printf("   Query: %s\n\n", query)

	// Create streamable-http client to connect to vmcp server
	mcpClient, err := client.NewStreamableHttpClient(
		serverURL,
		transport.WithHTTPTimeout(30*time.Second),
		transport.WithContinuousListening(),
	)
	if err != nil {
		fmt.Printf("❌ Failed to create MCP client: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := mcpClient.Close(); err != nil {
			fmt.Printf("⚠️  Error closing client: %v\n", err)
		}
	}()

	// Start the client connection
	if err := mcpClient.Start(ctx); err != nil {
		fmt.Printf("❌ Failed to start client connection: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Connected to vmcp server")

	// Initialize the client
	initResult, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "test-vmcp-client",
				Version: "1.0.0",
			},
			Capabilities: mcp.ClientCapabilities{},
		},
	})
	if err != nil {
		fmt.Printf("❌ Failed to initialize client: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Initialized - Server: %s %s\n\n", initResult.ServerInfo.Name, initResult.ServerInfo.Version)

	// List available tools to see if optim.find_tool is available
	fmt.Println("📋 Listing available tools...")
	toolsResult, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		fmt.Printf("❌ Failed to list tools: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Found %d tools:\n", len(toolsResult.Tools))
	hasFindTool := false
	for _, tool := range toolsResult.Tools {
		fmt.Printf("  - %s: %s\n", tool.Name, tool.Description)
		if tool.Name == "optim.find_tool" {
			hasFindTool = true
		}
	}
	fmt.Println()

	if !hasFindTool {
		fmt.Println("⚠️  Warning: optim.find_tool not found in available tools")
		fmt.Println("   The optimizer may not be enabled on this vmcp server")
		fmt.Println("   Continuing anyway...\n")
	}

	// Call optim.find_tool
	fmt.Printf("🔍 Calling optim.find_tool with query: %s\n\n", query)

	callResult, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": query,
				"tool_keywords":    "pull request",
				"limit":            20,
			},
		},
	})
	if err != nil {
		fmt.Printf("❌ Failed to call optim.find_tool: %v\n", err)
		os.Exit(1)
	}

	if callResult.IsError {
		fmt.Printf("❌ Tool call returned an error\n")
		if len(callResult.Content) > 0 {
			if textContent, ok := mcp.AsTextContent(callResult.Content[0]); ok {
				fmt.Printf("Error: %s\n", textContent.Text)
			}
		}
		os.Exit(1)
	}

	fmt.Println("✅ Successfully called optim.find_tool!")
	fmt.Println("\n📊 Results:")

	// Parse and display the result
	if len(callResult.Content) > 0 {
		if textContent, ok := mcp.AsTextContent(callResult.Content[0]); ok {
			// Try to parse as JSON for pretty printing
			var resultData map[string]any
			if err := json.Unmarshal([]byte(textContent.Text), &resultData); err == nil {
				// Pretty print JSON
				prettyJSON, err := json.MarshalIndent(resultData, "", "  ")
				if err == nil {
					fmt.Println(string(prettyJSON))
				} else {
					fmt.Println(textContent.Text)
				}
			} else {
				// Not JSON, print as-is
				fmt.Println(textContent.Text)
			}
		} else {
			// Not text content, print raw
			fmt.Printf("%+v\n", callResult.Content)
		}
	} else {
		fmt.Println("(No content returned)")
	}
}

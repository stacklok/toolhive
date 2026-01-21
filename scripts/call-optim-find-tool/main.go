// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
		fmt.Println("Usage: go run main.go <tool_description> [tool_keywords] [limit] [server_url]")
		fmt.Println("Example: go run main.go 'search the web' 'web search' 20")
		fmt.Println("Default server URL: http://localhost:4483/mcp")
		os.Exit(1)
	}

	toolDescription := os.Args[1]
	toolKeywords := ""
	if len(os.Args) >= 3 {
		toolKeywords = os.Args[2]
	}
	limit := 20
	if len(os.Args) >= 4 {
		if l, err := fmt.Sscanf(os.Args[3], "%d", &limit); err != nil || l != 1 {
			fmt.Printf("Invalid limit: %s, using default 20\n", os.Args[3])
			limit = 20
		}
	}
	serverURL := "http://localhost:4483/mcp"
	if len(os.Args) >= 5 {
		serverURL = os.Args[4]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	// Initialize the client
	initResult, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "optim-find-tool-client",
				Version: "1.0.0",
			},
			Capabilities: mcp.ClientCapabilities{},
		},
	})
	if err != nil {
		fmt.Printf("❌ Failed to initialize client: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Connected to: %s %s\n", initResult.ServerInfo.Name, initResult.ServerInfo.Version)

	// Call optim.find_tool
	args := map[string]any{
		"tool_description": toolDescription,
		"limit":            limit,
	}
	if toolKeywords != "" {
		args["tool_keywords"] = toolKeywords
	}

	callResult, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "optim.find_tool",
			Arguments: args,
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
				fmt.Println(textContent.Text)
			}
		} else {
			fmt.Printf("%+v\n", callResult.Content)
		}
	} else {
		fmt.Println("(No content returned)")
	}
}

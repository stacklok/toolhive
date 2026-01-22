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
	"path/filepath"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/embeddings"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <query>")
		fmt.Println("Example: go run main.go 'read pull requests from GitHub'")
		os.Exit(1)
	}

	query := os.Args[1]
	ctx := context.Background()
	tmpDir := filepath.Join(os.TempDir(), "optimizer-test")
	os.MkdirAll(tmpDir, 0755)

	fmt.Printf("🔍 Testing optim.find_tool with query: %s\n\n", query)

	// Create MCP server
	mcpServer := server.NewMCPServer("test-server", "1.0")

	// Create mock backend client
	mockClient := &mockBackendClient{}

	// Configure optimizer
	optimizerConfig := &optimizer.Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
		},
	}

	// Create optimizer integration
	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := optimizer.NewIntegration(ctx, optimizerConfig, mcpServer, mockClient, sessionMgr)
	if err != nil {
		fmt.Printf("❌ Failed to create optimizer integration: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = integration.Close() }()

	fmt.Println("✅ Optimizer integration created")

	// Ingest some test tools
	backends := []vmcp.Backend{
		{
			ID:            "github",
			Name:          "GitHub",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	err = integration.IngestInitialBackends(ctx, backends)
	if err != nil {
		fmt.Printf("⚠️  Failed to ingest initial backends: %v (continuing...)\n", err)
	}

	// Create a test session
	sessionID := "test-session-123"
	testSession := &mockSession{sessionID: sessionID}

	// Create capabilities with GitHub tools
	capabilities := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{
				Name:        "github_pull_request_read",
				Description: "Read details of a pull request from GitHub",
				BackendID:   "github",
			},
			{
				Name:        "github_issue_read",
				Description: "Read details of an issue from GitHub",
				BackendID:   "github",
			},
			{
				Name:        "github_pull_request_list",
				Description: "List pull requests in a GitHub repository",
				BackendID:   "github",
			},
		},
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"github_pull_request_read": {
					WorkloadID:   "github",
					WorkloadName: "GitHub",
				},
				"github_issue_read": {
					WorkloadID:   "github",
					WorkloadName: "GitHub",
				},
				"github_pull_request_list": {
					WorkloadID:   "github",
					WorkloadName: "GitHub",
				},
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	// Register session with MCP server first (needed for RegisterTools)
	err = mcpServer.RegisterSession(ctx, testSession)
	if err != nil {
		fmt.Printf("⚠️  Failed to register session: %v\n", err)
	}

	// Generate embeddings for session
	err = integration.OnRegisterSession(ctx, testSession, capabilities)
	if err != nil {
		fmt.Printf("❌ Failed to generate embeddings: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Embeddings generated for session")

	// Skip RegisterTools since we're calling the handler directly
	// RegisterTools requires per-session tool support which the mock doesn't have
	// err = integration.RegisterTools(ctx, testSession)
	// if err != nil {
	// 	fmt.Printf("⚠️  Failed to register optimizer tools: %v (skipping, calling handler directly)\n", err)
	// }
	fmt.Println("⏭️  Skipping tool registration (testing handler directly)")

	// Now try to call optim.find_tool directly via the handler
	fmt.Printf("\n🔍 Calling optim.find_tool handler directly...\n\n")

	// Create a context with capabilities (needed for the handler)
	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	// Create the tool call request
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": query,
				"tool_keywords":    "github pull request",
				"limit":            10,
			},
		},
	}

	// Call the handler directly using the exported test method
	handler := integration.CreateFindToolHandler()
	result, err := handler(ctxWithCaps, request)
	if err != nil {
		fmt.Printf("❌ Failed to call optim.find_tool: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n✅ Successfully called optim.find_tool!")
	fmt.Println("\n📊 Results:")
	
	// Print the result - CallToolResult has Content field which is a slice
	resultJSON, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling result: %v\n", err)
		fmt.Printf("Raw result: %+v\n", result)
	} else {
		fmt.Println(string(resultJSON))
	}
}

type mockBackendClient struct{}

func (m *mockBackendClient) ListCapabilities(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	return &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "github_pull_request_read",
				Description: "Read details of a pull request from GitHub",
			},
			{
				Name:        "github_issue_read",
				Description: "Read details of an issue from GitHub",
			},
			{
				Name:        "github_pull_request_list",
				Description: "List pull requests in a GitHub repository",
			},
		},
	}, nil
}

func (m *mockBackendClient) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

func (m *mockBackendClient) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (string, error) {
	return "", nil
}

func (m *mockBackendClient) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) ([]byte, error) {
	return nil, nil
}

type mockSession struct {
	sessionID string
}

func (m *mockSession) SessionID() string {
	return m.sessionID
}

func (m *mockSession) Send(_ interface{}) error {
	return nil
}

func (m *mockSession) Close() error {
	return nil
}

func (m *mockSession) Initialize() {}

func (m *mockSession) Initialized() bool {
	return true
}

func (m *mockSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	ch := make(chan mcp.JSONRPCNotification, 1)
	return ch
}

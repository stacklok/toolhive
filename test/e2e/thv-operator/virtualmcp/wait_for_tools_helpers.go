// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package virtualmcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// WaitForExpectedTools creates MCP sessions with retry until the validateTools
// function returns nil (all expected tools are present). Returns the final tool list.
// This is essential for avoiding flaky tests caused by session-scoped tool discovery
// race conditions: when a backend isn't fully ready, it's silently skipped, producing
// incomplete tool lists. Each retry creates a new MCP session to trigger fresh discovery.
func WaitForExpectedTools(
	vmcpNodePort int32,
	clientName string,
	validateTools func([]mcp.Tool) error,
	timeout ...time.Duration,
) *mcp.ListToolsResult {
	eventuallyTimeout := 2 * time.Minute
	if len(timeout) > 0 {
		eventuallyTimeout = timeout[0]
	}

	var tools *mcp.ListToolsResult
	gomega.Eventually(func() error {
		mcpClient, err := CreateInitializedMCPClient(vmcpNodePort, clientName, 30*time.Second)
		if err != nil {
			return fmt.Errorf("failed to create MCP client: %w", err)
		}
		defer mcpClient.Close()

		listRequest := mcp.ListToolsRequest{}
		tools, err = mcpClient.Client.ListTools(mcpClient.Ctx, listRequest)
		if err != nil {
			return fmt.Errorf("failed to list tools: %w", err)
		}
		return validateTools(tools.Tools)
	}, eventuallyTimeout, 5*time.Second).Should(gomega.Succeed())
	return tools
}

// WaitForExpectedToolsWithAuth creates authenticated MCP sessions with retry until the
// validateTools function returns nil (all expected tools are present). Returns the final
// tool list. This variant accepts StreamableHTTPCOptions for authenticated clients.
// The returned *mcpclient.Client is still open and must be closed by the caller so
// that subsequent tool calls can reuse the same session.
func WaitForExpectedToolsWithAuth(
	vmcpNodePort int32,
	timeout time.Duration,
	validateTools func([]mcp.Tool) error,
	opts ...transport.StreamableHTTPCOption,
) (*mcp.ListToolsResult, *mcpclient.Client) {
	var tools *mcp.ListToolsResult
	var mcpClient *mcpclient.Client

	// Ensure the last client is cleaned up if Eventually exhausts retries and
	// Ginkgo panics before the caller can defer Close().
	ginkgo.DeferCleanup(func() {
		if mcpClient != nil {
			_ = mcpClient.Close()
		}
	})

	serverURL := fmt.Sprintf("http://localhost:%d/mcp", vmcpNodePort)

	gomega.Eventually(func() error {
		// Close any previous client to avoid stale session state
		if mcpClient != nil {
			_ = mcpClient.Close()
		}

		var err error
		mcpClient, err = mcpclient.NewStreamableHttpClient(serverURL, opts...)
		if err != nil {
			return fmt.Errorf("failed to create client: %w", err)
		}

		initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer initCancel()

		if err := mcpClient.Start(initCtx); err != nil {
			return fmt.Errorf("failed to start transport: %w", err)
		}

		initRequest := mcp.InitializeRequest{}
		initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		initRequest.Params.ClientInfo = mcp.Implementation{
			Name:    "toolhive-e2e-test",
			Version: "1.0.0",
		}

		_, err = mcpClient.Initialize(initCtx, initRequest)
		if err != nil {
			return fmt.Errorf("failed to initialize: %w", err)
		}

		listCtx, listCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer listCancel()

		listRequest := mcp.ListToolsRequest{}
		tools, err = mcpClient.ListTools(listCtx, listRequest)
		if err != nil {
			return fmt.Errorf("failed to list tools: %w", err)
		}
		return validateTools(tools.Tools)
	}, timeout, 5*time.Second).Should(gomega.Succeed())

	return tools, mcpClient
}

// ToolsContainAll checks if the tool list contains all expected tool names (exact match).
// Returns an error listing missing tools, or nil if all are found.
func ToolsContainAll(tools []mcp.Tool, expectedNames ...string) error {
	nameSet := make(map[string]bool, len(tools))
	for _, t := range tools {
		nameSet[t.Name] = true
	}
	var missing []string
	for _, name := range expectedNames {
		if !nameSet[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing expected tools %v; got %v", missing, toolNames(tools))
	}
	return nil
}

// ToolsContainSubstring checks if the tool list contains at least one tool whose
// name contains each of the given substrings. Returns an error if any substring
// has no matching tool.
func ToolsContainSubstring(tools []mcp.Tool, substrings ...string) error {
	var missing []string
	for _, sub := range substrings {
		found := false
		for _, t := range tools {
			if strings.Contains(t.Name, sub) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, sub)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no tools matching substrings %v; got %v", missing, toolNames(tools))
	}
	return nil
}

// ToolsHavePrefix checks if there is at least one tool with each of the given prefixes.
// Returns an error listing missing prefixes, or nil if all are found.
func ToolsHavePrefix(tools []mcp.Tool, prefixes ...string) error {
	var missing []string
	for _, prefix := range prefixes {
		found := false
		for _, t := range tools {
			if strings.HasPrefix(t.Name, prefix) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, prefix)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no tools with prefixes %v; got %v", missing, toolNames(tools))
	}
	return nil
}

// toolNames extracts tool names from a slice of mcp.Tool for error messages.
func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

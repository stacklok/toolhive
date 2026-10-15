// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testConnectorURL = "https://cgw.example.com/gw/mcp"

// newConnectorManager returns a Claude Desktop manager on which "thv llm setup"
// has already run, plus the path of ToolHive's configuration document.
func newConnectorManager(t *testing.T) (*ClientManager, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Claude Desktop LLM gateway setup is POSIX-only")
	}
	cm, _ := newClaudeDesktopManager(t)
	configPath, err := cm.ConfigureLLMGateway(ClientApp(ClaudeDesktop), claudeDesktopApplyCfg())
	require.NoError(t, err)
	return cm, configPath
}

// readDocMap decodes a configuration document without dropping unknown keys.
func readDocMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-controlled path
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

func TestAddConnector_WritesOAuthEntryAndKeepsInferenceSettings(t *testing.T) {
	t.Parallel()
	cm, configPath := newConnectorManager(t)

	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL))

	doc := readDocMap(t, configPath)
	assert.Equal(t, []any{map[string]any{
		"name":      "stacklok",
		"transport": "http",
		"url":       testConnectorURL,
		"oauth":     true,
	}}, doc["managedMcpServers"])
	assert.Equal(t, "gateway", doc["inferenceProvider"], "inference settings must be preserved")
	assert.Equal(t, "https://gw.example.com/anthropic", doc["inferenceGatewayBaseUrl"])
}

func TestAddConnector_SameNameUpdatesURL(t *testing.T) {
	t.Parallel()
	cm, _ := newConnectorManager(t)

	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL))
	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "other", "https://other.example.com/mcp"))
	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", "https://new.example.com/mcp"))

	connectors, err := cm.ListConnectors(ClientApp(ClaudeDesktop))
	require.NoError(t, err)
	assert.Equal(t, []Connector{
		{Name: "stacklok", URL: "https://new.example.com/mcp"},
		{Name: "other", URL: "https://other.example.com/mcp"},
	}, connectors)
}

func TestAddConnector_PreservesEntriesToolHiveDidNotWrite(t *testing.T) {
	t.Parallel()
	cm, configPath := newConnectorManager(t)

	// A user's own entry, e.g. a local stdio server added in the in-app window.
	doc := readDocMap(t, configPath)
	userEntry := map[string]any{"name": "local", "transport": "stdio", "command": "/usr/local/bin/srv"}
	doc["managedMcpServers"] = []any{userEntry}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o600))

	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL))

	servers := readDocMap(t, configPath)["managedMcpServers"].([]any)
	require.Len(t, servers, 2)
	assert.Equal(t, userEntry, servers[0])

	connectors, err := cm.ListConnectors(ClientApp(ClaudeDesktop))
	require.NoError(t, err)
	assert.Equal(t, []Connector{{Name: "stacklok", URL: testConnectorURL}}, connectors,
		"list shows remote servers only")
}

// writeServers replaces the document's managedMcpServers with servers.
func writeServers(t *testing.T, configPath string, servers ...any) {
	t.Helper()
	doc := readDocMap(t, configPath)
	doc["managedMcpServers"] = servers
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o600))
}

func TestAddConnector_SameNameKeepsOtherFields(t *testing.T) {
	t.Parallel()
	cm, configPath := newConnectorManager(t)
	writeServers(t, configPath, map[string]any{
		"name":       "stacklok",
		"transport":  "http",
		"url":        testConnectorURL,
		"oauth":      map[string]any{"clientId": "abc"},
		"toolPolicy": map[string]any{"*": "ask"},
	})

	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", "https://new.example.com/mcp"))

	assert.Equal(t, []any{map[string]any{
		"name":       "stacklok",
		"transport":  "http",
		"url":        "https://new.example.com/mcp",
		"oauth":      map[string]any{"clientId": "abc"},
		"toolPolicy": map[string]any{"*": "ask"},
	}}, readDocMap(t, configPath)["managedMcpServers"])
}

func TestConnectors_LeaveSameNameLocalEntryAlone(t *testing.T) {
	t.Parallel()
	local := map[string]any{"name": "stacklok", "transport": "stdio", "command": "/usr/local/bin/srv"}

	t.Run("add rejects the name collision", func(t *testing.T) {
		t.Parallel()
		cm, configPath := newConnectorManager(t)
		writeServers(t, configPath, local)

		assert.Error(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL))
		assert.Equal(t, []any{local}, readDocMap(t, configPath)["managedMcpServers"])
	})
	t.Run("rm does not delete the local entry", func(t *testing.T) {
		t.Parallel()
		cm, configPath := newConnectorManager(t)
		writeServers(t, configPath, local)

		assert.ErrorIs(t, cm.RemoveConnector(ClientApp(ClaudeDesktop), "stacklok"), ErrConnectorNotFound)
		assert.Equal(t, []any{local}, readDocMap(t, configPath)["managedMcpServers"])
	})
}

func TestAddConnector_RejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		connName  string
		serverURL string
	}{
		{name: "empty name", connName: "", serverURL: testConnectorURL},
		{name: "padded name", connName: " stacklok", serverURL: testConnectorURL},
		{name: "http URL", connName: "stacklok", serverURL: "http://cgw.example.com/gw/mcp"},
		{name: "relative URL", connName: "stacklok", serverURL: "/gw/mcp"},
		{name: "no host", connName: "stacklok", serverURL: "https:///gw/mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cm, configPath := newConnectorManager(t)

			err := cm.AddConnector(ClientApp(ClaudeDesktop), tt.connName, tt.serverURL)
			assert.Error(t, err)
			assert.NotContains(t, readDocMap(t, configPath), "managedMcpServers", "nothing should be written")
		})
	}
}

func TestAddConnector_RequiresLLMSetup(t *testing.T) {
	t.Parallel()
	cm, _ := newClaudeDesktopManager(t)

	err := cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL)
	assert.ErrorIs(t, err, ErrConnectorsNotSetUp)

	_, err = cm.ListConnectors(ClientApp(ClaudeDesktop))
	assert.ErrorIs(t, err, ErrConnectorsNotSetUp)
}

func TestAddConnector_RejectsUnsupportedClient(t *testing.T) {
	t.Parallel()
	cm, _ := newClaudeDesktopManager(t)

	err := cm.AddConnector(ClaudeCode, "stacklok", testConnectorURL)
	assert.ErrorIs(t, err, ErrConnectorsUnsupported)
}

func TestRemoveConnector(t *testing.T) {
	t.Parallel()
	cm, configPath := newConnectorManager(t)

	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL))
	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "other", "https://other.example.com/mcp"))

	require.NoError(t, cm.RemoveConnector(ClientApp(ClaudeDesktop), "stacklok"))
	connectors, err := cm.ListConnectors(ClientApp(ClaudeDesktop))
	require.NoError(t, err)
	assert.Equal(t, []Connector{{Name: "other", URL: "https://other.example.com/mcp"}}, connectors)

	require.NoError(t, cm.RemoveConnector(ClientApp(ClaudeDesktop), "other"))
	assert.NotContains(t, readDocMap(t, configPath), "managedMcpServers",
		"removing the last connector drops the key")

	err = cm.RemoveConnector(ClientApp(ClaudeDesktop), "other")
	assert.ErrorIs(t, err, ErrConnectorNotFound)
}

func TestListConnectors_RejectsMalformedServers(t *testing.T) {
	t.Parallel()
	cm, configPath := newConnectorManager(t)

	doc := readDocMap(t, configPath)
	doc["managedMcpServers"] = "not-an-array"
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, data, 0o600))

	_, err = cm.ListConnectors(ClientApp(ClaudeDesktop))
	assert.Error(t, err)
	assert.Error(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL),
		"a write must not overwrite a value it does not understand")
}

func TestRevertLLMGateway_RemovesConnectorsWithConfig(t *testing.T) {
	t.Parallel()
	cm, configPath := newConnectorManager(t)
	require.NoError(t, cm.AddConnector(ClientApp(ClaudeDesktop), "stacklok", testConnectorURL))
	assert.Equal(t, []string{"stacklok"}, connectorNamesInDoc(configPath))

	require.NoError(t, cm.RevertLLMGateway(ClientApp(ClaudeDesktop), configPath))

	assert.NoFileExists(t, configPath)
	_, err := cm.ListConnectors(ClientApp(ClaudeDesktop))
	assert.ErrorIs(t, err, ErrConnectorsNotSetUp)
}

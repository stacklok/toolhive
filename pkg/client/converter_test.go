package client

import (
	"reflect"
	"testing"
)

const invalidConfig = "invalid"

// Helper function to create a Goose-style config
func createGooseConfig() *mcpClientConfig {
	return &mcpClientConfig{
		ClientType:           Goose,
		MCPServersPathPrefix: "/extensions",
		MCPServersUrlLabel:   "uri",
		YAMLStorageType:      "map",
		YAMLDefaults: map[string]interface{}{
			"enabled": true,
			"timeout": 60,
		},
	}
}

// Helper function to create a Continue-style config
func createContinueConfig() *mcpClientConfig {
	return &mcpClientConfig{
		ClientType:           Continue,
		MCPServersPathPrefix: "/mcpServers",
		MCPServersUrlLabel:   "url",
		YAMLStorageType:      "array",
		YAMLIdentifierField:  "name",
	}
}

func TestGenericYAMLConverter_ConvertFromMCPServer_Goose(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createGooseConfig())

	tests := []struct {
		name       string
		serverName string
		server     MCPServer
		expected   map[string]interface{}
	}{
		{
			name:       "basic conversion with Url",
			serverName: "test-server",
			server: MCPServer{
				Type: "mcp",
				Url:  "http://example.com",
			},
			expected: map[string]interface{}{
				"name":    "test-server",
				"enabled": true,
				"type":    "mcp",
				"timeout": 60,
				"uri":     "http://example.com",
			},
		},
		{
			name:       "with ServerUrl field",
			serverName: "another-server",
			server: MCPServer{
				Type:      "custom",
				ServerUrl: "https://api.example.com",
			},
			expected: map[string]interface{}{
				"name":    "another-server",
				"enabled": true,
				"type":    "custom",
				"timeout": 60,
				"uri":     "https://api.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := converter.ConvertFromMCPServer(tt.serverName, tt.server)
			if err != nil {
				t.Fatalf("ConvertFromMCPServer() error = %v", err)
			}

			resultMap, ok := result.(map[string]interface{})
			if !ok {
				t.Fatalf("ConvertFromMCPServer() returned wrong type, got %T", result)
			}

			if !reflect.DeepEqual(resultMap, tt.expected) {
				t.Errorf("ConvertFromMCPServer() = %+v, want %+v", resultMap, tt.expected)
			}
		})
	}
}

func TestGenericYAMLConverter_ConvertFromMCPServer_Continue(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createContinueConfig())

	tests := []struct {
		name       string
		serverName string
		server     MCPServer
		expected   map[string]interface{}
	}{
		{
			name:       "basic conversion",
			serverName: "test-server",
			server: MCPServer{
				Type: "sse",
				Url:  "http://example.com",
			},
			expected: map[string]interface{}{
				"name": "test-server",
				"type": "sse",
				"url":  "http://example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := converter.ConvertFromMCPServer(tt.serverName, tt.server)
			if err != nil {
				t.Fatalf("ConvertFromMCPServer() error = %v", err)
			}

			resultMap, ok := result.(map[string]interface{})
			if !ok {
				t.Fatalf("ConvertFromMCPServer() returned wrong type, got %T", result)
			}

			if !reflect.DeepEqual(resultMap, tt.expected) {
				t.Errorf("ConvertFromMCPServer() = %+v, want %+v", resultMap, tt.expected)
			}
		})
	}
}

func TestGenericYAMLConverter_UpsertEntry_MapStorage(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createGooseConfig())

	t.Run("upsert in empty config", func(t *testing.T) {
		t.Parallel()
		config := make(map[string]interface{})
		entry := map[string]interface{}{
			"name":    "test-server",
			"enabled": true,
			"type":    "mcp",
			"timeout": 30,
			"uri":     "http://example.com",
		}

		err := converter.UpsertEntry(config, "test-server", entry)
		if err != nil {
			t.Fatalf("UpsertEntry() error = %v", err)
		}

		extensions, ok := config["extensions"].(map[string]interface{})
		if !ok {
			t.Fatal("extensions not found or wrong type")
		}

		serverConfig, exists := extensions["test-server"]
		if !exists {
			t.Fatal("server entry not found")
		}

		serverMap, ok := serverConfig.(map[string]interface{})
		if !ok {
			t.Fatal("server config is not a map")
		}

		if !reflect.DeepEqual(serverMap, entry) {
			t.Errorf("UpsertEntry() result = %+v, want %+v", serverMap, entry)
		}
	})

	t.Run("upsert in existing config", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"extensions": map[string]interface{}{
				"existing-server": map[string]interface{}{
					"name":    "existing-server",
					"enabled": false,
					"type":    "old",
				},
			},
		}

		entry := map[string]interface{}{
			"name":    "new-server",
			"enabled": true,
			"type":    "mcp",
			"timeout": 60,
			"uri":     "https://new.example.com",
		}

		err := converter.UpsertEntry(config, "new-server", entry)
		if err != nil {
			t.Fatalf("UpsertEntry() error = %v", err)
		}

		extensions := config["extensions"].(map[string]interface{})

		// Check existing server is preserved
		if _, exists := extensions["existing-server"]; !exists {
			t.Error("existing server was removed")
		}

		// Check new server was added
		newServer, exists := extensions["new-server"]
		if !exists {
			t.Fatal("new server was not added")
		}

		newServerMap := newServer.(map[string]interface{})
		if !reflect.DeepEqual(newServerMap, entry) {
			t.Errorf("new server config = %+v, want %+v", newServerMap, entry)
		}
	})

	t.Run("invalid config type", func(t *testing.T) {
		t.Parallel()
		config := invalidConfig
		entry := map[string]interface{}{"name": "test"}

		err := converter.UpsertEntry(config, "test", entry)
		if err == nil {
			t.Error("UpsertEntry() should have returned error for invalid config type")
		}
	})
}

func TestGenericYAMLConverter_UpsertEntry_ArrayStorage(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createContinueConfig())

	t.Run("upsert in empty config", func(t *testing.T) {
		t.Parallel()
		config := make(map[string]interface{})
		entry := map[string]interface{}{
			"name": "test-server",
			"type": "sse",
			"url":  "http://example.com",
		}

		err := converter.UpsertEntry(config, "test-server", entry)
		if err != nil {
			t.Fatalf("UpsertEntry() error = %v", err)
		}

		servers, ok := config["mcpServers"].([]interface{})
		if !ok {
			t.Fatal("mcpServers not found or wrong type")
		}

		if len(servers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(servers))
		}

		serverMap := servers[0].(map[string]interface{})
		if !reflect.DeepEqual(serverMap, entry) {
			t.Errorf("UpsertEntry() result = %+v, want %+v", serverMap, entry)
		}
	})

	t.Run("update existing server in array", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"mcpServers": []interface{}{
				map[string]interface{}{
					"name": "test-server",
					"type": "old",
					"url":  "http://old.com",
				},
			},
		}

		entry := map[string]interface{}{
			"name": "test-server",
			"type": "new",
			"url":  "http://new.com",
		}

		err := converter.UpsertEntry(config, "test-server", entry)
		if err != nil {
			t.Fatalf("UpsertEntry() error = %v", err)
		}

		servers := config["mcpServers"].([]interface{})
		if len(servers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(servers))
		}

		serverMap := servers[0].(map[string]interface{})
		if !reflect.DeepEqual(serverMap, entry) {
			t.Errorf("updated server = %+v, want %+v", serverMap, entry)
		}
	})
}

func TestGenericYAMLConverter_RemoveEntry_MapStorage(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createGooseConfig())

	t.Run("remove from existing config", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"extensions": map[string]interface{}{
				"server1": map[string]interface{}{"name": "server1"},
				"server2": map[string]interface{}{"name": "server2"},
			},
		}

		err := converter.RemoveEntry(config, "server1")
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		extensions := config["extensions"].(map[string]interface{})

		// Check server1 was removed
		if _, exists := extensions["server1"]; exists {
			t.Error("server1 should have been removed")
		}

		// Check server2 still exists
		if _, exists := extensions["server2"]; !exists {
			t.Error("server2 should still exist")
		}
	})

	t.Run("remove from config without extensions", func(t *testing.T) {
		t.Parallel()
		config := make(map[string]interface{})

		err := converter.RemoveEntry(config, "nonexistent")
		if err != nil {
			t.Fatalf("RemoveEntry() should not error when extensions don't exist, got: %v", err)
		}
	})

	t.Run("invalid config type", func(t *testing.T) {
		t.Parallel()
		config := invalidConfig

		err := converter.RemoveEntry(config, "test")
		if err == nil {
			t.Error("RemoveEntry() should have returned error for invalid config type")
		}
	})
}

func TestGenericYAMLConverter_RemoveEntry_ArrayStorage(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createContinueConfig())

	t.Run("remove from existing array", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"mcpServers": []interface{}{
				map[string]interface{}{"name": "server1"},
				map[string]interface{}{"name": "server2"},
			},
		}

		err := converter.RemoveEntry(config, "server1")
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		servers := config["mcpServers"].([]interface{})
		if len(servers) != 1 {
			t.Fatalf("expected 1 server remaining, got %d", len(servers))
		}

		remainingServer := servers[0].(map[string]interface{})
		if remainingServer["name"] != "server2" {
			t.Error("wrong server was removed")
		}
	})

	t.Run("remove from config without servers", func(t *testing.T) {
		t.Parallel()
		config := make(map[string]interface{})

		err := converter.RemoveEntry(config, "nonexistent")
		if err != nil {
			t.Fatalf("RemoveEntry() should not error when servers don't exist, got: %v", err)
		}
	})
}

package client

import (
	"reflect"
	"testing"
)

const (
	invalidConfig = "invalid"
	testServer1   = "server1"
	testServer2   = "server2"
)

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

func TestGenericYAMLConverter_UpsertEntry_InvalidEntry(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createGooseConfig())

	t.Run("invalid entry type", func(t *testing.T) {
		t.Parallel()
		config := make(map[string]interface{})
		entry := "invalid entry" // Not a map

		err := converter.UpsertEntry(config, "test", entry)
		if err == nil {
			t.Error("UpsertEntry() should have returned error for invalid entry type")
		}
		if err.Error() != "entry must be a map[string]interface{}" {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestGenericYAMLConverter_UpsertEntry_MapStorage_InvalidServers(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createGooseConfig())

	t.Run("servers is not a map", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"extensions": "invalid", // Not a map
		}
		entry := map[string]interface{}{
			"name": "test-server",
		}

		err := converter.UpsertEntry(config, "test-server", entry)
		if err != nil {
			t.Fatalf("UpsertEntry() should handle invalid servers type, got error: %v", err)
		}

		// Should have replaced invalid servers with proper map
		extensions, ok := config["extensions"].(map[string]interface{})
		if !ok {
			t.Fatal("extensions should be a map now")
		}

		if _, exists := extensions["test-server"]; !exists {
			t.Error("server should have been added")
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
				testServer1: map[string]interface{}{"name": testServer1},
				testServer2: map[string]interface{}{"name": testServer2},
			},
		}

		err := converter.RemoveEntry(config, testServer1)
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		extensions := config["extensions"].(map[string]interface{})

		// Check server1 was removed
		if _, exists := extensions[testServer1]; exists {
			t.Error("server1 should have been removed")
		}

		// Check server2 still exists
		if _, exists := extensions[testServer2]; !exists {
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

	t.Run("remove with non-map servers", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"extensions": []interface{}{"invalid", "format"}, // Not a map
		}

		err := converter.RemoveEntry(config, testServer1)
		if err == nil {
			t.Error("RemoveEntry() should have returned error for non-map servers")
		}
		if err.Error() != "invalid servers format" {
			t.Errorf("unexpected error message: %v", err)
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
				map[string]interface{}{"name": testServer1},
				map[string]interface{}{"name": testServer2},
			},
		}

		err := converter.RemoveEntry(config, testServer1)
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		servers := config["mcpServers"].([]interface{})
		if len(servers) != 1 {
			t.Fatalf("expected 1 server remaining, got %d", len(servers))
		}

		remainingServer := servers[0].(map[string]interface{})
		if remainingServer["name"] != testServer2 {
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

	t.Run("remove with typed map array", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"mcpServers": []map[string]interface{}{
				{"name": testServer1, "type": "sse"},
				{"name": testServer2, "type": "stdio"},
			},
		}

		err := converter.RemoveEntry(config, testServer1)
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		servers := config["mcpServers"].([]interface{})
		if len(servers) != 1 {
			t.Fatalf("expected 1 server remaining, got %d", len(servers))
		}

		remainingServer := servers[0].(map[string]interface{})
		if remainingServer["name"] != testServer2 {
			t.Error("wrong server was removed")
		}
	})

	t.Run("remove with non-map entry in array", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"mcpServers": []interface{}{
				map[string]interface{}{"name": testServer1},
				"invalid-entry", // Not a map - should be preserved
				map[string]interface{}{"name": testServer2},
			},
		}

		err := converter.RemoveEntry(config, testServer1)
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		servers := config["mcpServers"].([]interface{})
		if len(servers) != 2 {
			t.Fatalf("expected 2 entries remaining, got %d", len(servers))
		}

		// First entry should be the non-map entry
		if servers[0] != "invalid-entry" {
			t.Error("invalid-entry should be preserved")
		}

		// Second entry should be server2
		remainingServer := servers[1].(map[string]interface{})
		if remainingServer["name"] != testServer2 {
			t.Error("wrong server was removed")
		}
	})

	t.Run("remove with invalid servers type", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"mcpServers": "invalid-type", // Not an array
		}

		err := converter.RemoveEntry(config, testServer1)
		// Should return nil (nothing to remove) when servers is not an array type
		if err != nil {
			t.Fatalf("RemoveEntry() should handle invalid servers type gracefully, got error: %v", err)
		}
	})
}

func TestGenericYAMLConverter_UpsertEntry_ArrayStorage_TypedMapArray(t *testing.T) {
	t.Parallel()
	converter := NewGenericYAMLConverter(createContinueConfig())

	t.Run("upsert with typed map array", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"mcpServers": []map[string]interface{}{
				{"name": "existing-server", "type": "old"},
			},
		}

		entry := map[string]interface{}{
			"name": "new-server",
			"type": "sse",
			"url":  "http://new.com",
		}

		err := converter.UpsertEntry(config, "new-server", entry)
		if err != nil {
			t.Fatalf("UpsertEntry() error = %v", err)
		}

		servers := config["mcpServers"].([]interface{})
		if len(servers) != 2 {
			t.Fatalf("expected 2 servers, got %d", len(servers))
		}

		// Check new server was added
		newServer := servers[1].(map[string]interface{})
		if !reflect.DeepEqual(newServer, entry) {
			t.Errorf("new server = %+v, want %+v", newServer, entry)
		}
	})
}

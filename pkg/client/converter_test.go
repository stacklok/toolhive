package client

import (
	"reflect"
	"testing"
)

const invalidConfig = "invalid"

func TestGooseYAMLConverter_ConvertFromMCPServer(t *testing.T) {
	t.Parallel()
	converter := &GooseYAMLConverter{}

	tests := []struct {
		name       string
		serverName string
		server     MCPServer
		expected   GooseExtension
	}{
		{
			name:       "basic conversion",
			serverName: "test-server",
			server: MCPServer{
				Type: "mcp",
				Url:  "http://example.com",
			},
			expected: GooseExtension{
				Name:    "test-server",
				Enabled: true,
				Type:    "mcp",
				Timeout: GooseTimeout,
				Uri:     "http://example.com",
			},
		},
		{
			name:       "with ServerUrl field",
			serverName: "another-server",
			server: MCPServer{
				Type:      "custom",
				ServerUrl: "https://api.example.com",
			},
			expected: GooseExtension{
				Name:    "another-server",
				Enabled: true,
				Type:    "custom",
				Timeout: GooseTimeout,
				Uri:     "https://api.example.com",
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

			extension, ok := result.(GooseExtension)
			if !ok {
				t.Fatalf("ConvertFromMCPServer() returned wrong type, got %T", result)
			}

			if !reflect.DeepEqual(extension, tt.expected) {
				t.Errorf("ConvertFromMCPServer() = %+v, want %+v", extension, tt.expected)
			}
		})
	}
}

func TestGooseYAMLConverter_UpsertEntry(t *testing.T) {
	t.Parallel()
	converter := &GooseYAMLConverter{}

	t.Run("upsert in empty config", func(t *testing.T) {
		t.Parallel()
		config := make(map[string]interface{})
		entry := GooseExtension{
			Name:    "test-server",
			Enabled: true,
			Type:    "mcp",
			Timeout: 30,
			Uri:     "http://example.com",
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

		expectedMap := map[string]interface{}{
			"name":    "test-server",
			"enabled": true,
			"type":    "mcp",
			"timeout": 30,
			"uri":     "http://example.com",
		}

		if !reflect.DeepEqual(serverMap, expectedMap) {
			t.Errorf("UpsertEntry() result = %+v, want %+v", serverMap, expectedMap)
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

		entry := GooseExtension{
			Name:    "new-server",
			Enabled: true,
			Type:    "mcp",
			Timeout: 60,
			Uri:     "https://new.example.com",
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
		expectedMap := map[string]interface{}{
			"name":    "new-server",
			"enabled": true,
			"type":    "mcp",
			"timeout": 60,
			"uri":     "https://new.example.com",
		}

		if !reflect.DeepEqual(newServerMap, expectedMap) {
			t.Errorf("new server config = %+v, want %+v", newServerMap, expectedMap)
		}
	})

	t.Run("invalid config type", func(t *testing.T) {
		t.Parallel()
		config := invalidConfig
		entry := GooseExtension{Name: "test"}

		err := converter.UpsertEntry(config, "test", entry)
		if err == nil {
			t.Error("UpsertEntry() should have returned error for invalid config type")
		}
	})
}

func TestGooseYAMLConverter_RemoveEntry(t *testing.T) {
	t.Parallel()
	converter := &GooseYAMLConverter{}

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

	t.Run("remove nonexistent server", func(t *testing.T) {
		t.Parallel()
		config := map[string]interface{}{
			"extensions": map[string]interface{}{
				"server1": map[string]interface{}{"name": "server1"},
			},
		}

		err := converter.RemoveEntry(config, "nonexistent")
		if err != nil {
			t.Fatalf("RemoveEntry() error = %v", err)
		}

		// server1 should still exist
		extensions := config["extensions"].(map[string]interface{})
		if _, exists := extensions["server1"]; !exists {
			t.Error("existing server should not be affected")
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

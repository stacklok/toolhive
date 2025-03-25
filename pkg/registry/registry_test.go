package registry

import (
	"testing"
)

func TestGetRegistry(t *testing.T) {
	reg, err := GetRegistry()
	if err != nil {
		t.Fatalf("Failed to get registry: %v", err)
	}

	if reg == nil {
		t.Fatal("Registry is nil")
	}

	if reg.Version == "" {
		t.Error("Registry version is empty")
	}

	if reg.LastUpdated == "" {
		t.Error("Registry last updated is empty")
	}

	if len(reg.Servers) == 0 {
		t.Error("Registry has no servers")
	}
}

func TestGetServer(t *testing.T) {
	// Test getting an existing server
	server, err := GetServer("brave-search")
	if err != nil {
		t.Fatalf("Failed to get server: %v", err)
	}

	if server == nil {
		t.Fatal("Server is nil")
	}

	if server.Image == "" {
		t.Error("Server image is empty")
	}

	if server.Description == "" {
		t.Error("Server description is empty")
	}

	// Test getting a non-existent server
	_, err = GetServer("non-existent-server")
	if err == nil {
		t.Error("Expected error when getting non-existent server")
	}
}

func TestSearchServers(t *testing.T) {
	// Test searching for servers
	servers, err := SearchServers("search")
	if err != nil {
		t.Fatalf("Failed to search servers: %v", err)
	}

	if len(servers) == 0 {
		t.Error("No servers found for search query")
	}

	// Test searching for non-existent servers
	servers, err = SearchServers("non-existent-server")
	if err != nil {
		t.Fatalf("Failed to search servers: %v", err)
	}

	if len(servers) > 0 {
		t.Errorf("Expected no servers for non-existent query, got %d", len(servers))
	}
}

func TestListServers(t *testing.T) {
	servers, err := ListServers()
	if err != nil {
		t.Fatalf("Failed to list servers: %v", err)
	}

	if len(servers) == 0 {
		t.Error("No servers found")
	}

	// Verify that we get the same number of servers as in the registry
	reg, err := GetRegistry()
	if err != nil {
		t.Fatalf("Failed to get registry: %v", err)
	}

	if len(servers) != len(reg.Servers) {
		t.Errorf("Expected %d servers, got %d", len(reg.Servers), len(servers))
	}
}
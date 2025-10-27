package operator_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

// MockAPIServerBuilder provides a fluent interface for building mock API servers
type MockAPIServerBuilder struct {
	infoResponse    string
	serversResponse string
	serverDetails   map[string]string
	openapiResponse string
	customHandlers  map[string]http.HandlerFunc
}

// NewMockAPIServerBuilder creates a new mock API server builder
func NewMockAPIServerBuilder() *MockAPIServerBuilder {
	return &MockAPIServerBuilder{
		serverDetails:  make(map[string]string),
		customHandlers: make(map[string]http.HandlerFunc),
	}
}

// WithToolHiveInfo adds a ToolHive /v0/info endpoint response
func (b *MockAPIServerBuilder) WithToolHiveInfo(version, lastUpdated, source string, totalServers int) *MockAPIServerBuilder {
	b.infoResponse = fmt.Sprintf(`{
		"version": "%s",
		"last_updated": "%s",
		"source": "%s",
		"total_servers": %d
	}`, version, lastUpdated, source, totalServers)
	return b
}

// WithToolHiveServers adds a ToolHive /v0/servers endpoint response
func (b *MockAPIServerBuilder) WithToolHiveServers(servers []APIServerSummary) *MockAPIServerBuilder {
	serversJSON := "["
	for i, server := range servers {
		if i > 0 {
			serversJSON += ","
		}
		serversJSON += fmt.Sprintf(`{
			"name": "%s",
			"description": "%s",
			"tier": "%s",
			"status": "%s",
			"transport": "%s"
		}`, server.Name, server.Description, server.Tier, server.Status, server.Transport)
	}
	serversJSON += "]"

	b.serversResponse = fmt.Sprintf(`{
		"servers": %s,
		"total": %d
	}`, serversJSON, len(servers))
	return b
}

// WithServerDetail adds a ToolHive /v0/servers/{name} endpoint response
func (b *MockAPIServerBuilder) WithServerDetail(name, description, tier, status, transport, image string) *MockAPIServerBuilder {
	b.serverDetails[name] = fmt.Sprintf(`{
		"name": "%s",
		"description": "%s",
		"tier": "%s",
		"status": "%s",
		"transport": "%s",
		"image": "%s"
	}`, name, description, tier, status, transport, image)
	return b
}

// WithUpstreamOpenAPI adds an upstream /openapi.yaml endpoint response
func (b *MockAPIServerBuilder) WithUpstreamOpenAPI(version, description string) *MockAPIServerBuilder {
	b.openapiResponse = fmt.Sprintf(`
openapi: 3.1.0
info:
  title: Official MCP Registry
  description: |
    %s
  version: %s
`, description, version)
	return b
}

// WithCustomHandler adds a custom HTTP handler for a specific path
func (b *MockAPIServerBuilder) WithCustomHandler(path string, handler http.HandlerFunc) *MockAPIServerBuilder {
	b.customHandlers[path] = handler
	return b
}

// Build creates and starts the mock HTTP server
func (b *MockAPIServerBuilder) Build() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for custom handlers first
		if handler, exists := b.customHandlers[r.URL.Path]; exists {
			handler(w, r)
			return
		}

		// Handle standard endpoints
		switch r.URL.Path {
		case "/v0/info":
			if b.infoResponse != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, b.infoResponse)
				return
			}

		case "/v0/servers":
			if b.serversResponse != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, b.serversResponse)
				return
			}

		case "/openapi.yaml":
			if b.openapiResponse != "" {
				w.Header().Set("Content-Type", "application/x-yaml")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, b.openapiResponse)
				return
			}

		default:
			// Check if this is a server detail request
			for serverName, detailResponse := range b.serverDetails {
				if r.URL.Path == "/v0/servers/"+serverName {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, detailResponse)
					return
				}
			}
		}

		// Default 404 for unhandled paths
		w.WriteHeader(http.StatusNotFound)
	}))
}

// APIServerSummary represents a server summary for the mock API
type APIServerSummary struct {
	Name        string
	Description string
	Tier        string
	Status      string
	Transport   string
}

// CreateDefaultToolHiveServers creates a standard set of test servers for ToolHive API
func CreateDefaultToolHiveServers() []APIServerSummary {
	return []APIServerSummary{
		{
			Name:        "filesystem",
			Description: "Filesystem MCP server",
			Tier:        "official",
			Status:      "active",
			Transport:   "stdio",
		},
		{
			Name:        "github",
			Description: "GitHub MCP server",
			Tier:        "official",
			Status:      "active",
			Transport:   "stdio",
		},
	}
}

// NewToolHiveMockServer creates a fully configured mock ToolHive API server with default test data
func NewToolHiveMockServer() *httptest.Server {
	servers := CreateDefaultToolHiveServers()

	builder := NewMockAPIServerBuilder().
		WithToolHiveInfo("1.0.0", "2025-01-15T12:00:00Z", "http://test-api", len(servers)).
		WithToolHiveServers(servers)

	// Add server details
	for _, server := range servers {
		var image string
		switch server.Name {
		case "filesystem":
			image = "ghcr.io/modelcontextprotocol/server-filesystem:latest"
		case "github":
			image = "ghcr.io/modelcontextprotocol/server-github:latest"
		default:
			image = fmt.Sprintf("ghcr.io/test/%s:latest", server.Name)
		}

		builder.WithServerDetail(
			server.Name,
			server.Description,
			server.Tier,
			server.Status,
			server.Transport,
			image,
		)
	}

	return builder.Build()
}

// NewUpstreamMockServer creates a mock upstream MCP Registry API server
func NewUpstreamMockServer() *httptest.Server {
	return NewMockAPIServerBuilder().
		WithUpstreamOpenAPI(
			"1.0.0",
			"[GitHub repository](https://github.com/modelcontextprotocol/registry)",
		).
		Build()
}

package v1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	v1 "github.com/stacklok/toolhive/cmd/thv-registry-api/api/v1"
	"github.com/stacklok/toolhive/cmd/thv-registry-api/internal/service"
	"github.com/stacklok/toolhive/cmd/thv-registry-api/internal/service/mocks"
	"github.com/stacklok/toolhive/pkg/registry"
)

// Removed embedded test registry data - using inline JSON instead

// testRegistryJSON provides realistic representative server entries for testing
// This includes different server types, transports, tiers, and configurations
const testRegistryJSON = `{
  "version": "1.0.0",
  "last_updated": "2025-09-10T00:16:54Z",
  "servers": {
    "adb-mysql-mcp-server": {
      "description": "Official MCP server for AnalyticDB for MySQL of Alibaba Cloud",
      "tier": "Official",
      "status": "Active",
      "transport": "stdio",
      "tools": [
        "execute_sql",
        "get_query_plan",
        "get_execution_plan"
      ],
      "metadata": {
        "stars": 16,
        "pulls": 0,
        "last_updated": "2025-09-07T02:30:47Z"
      },
      "repository_url": "https://github.com/aliyun/alibabacloud-adb-mysql-mcp-server",
      "tags": [
        "database",
        "mysql",
        "analytics",
        "sql",
        "alibaba-cloud",
        "data-warehouse"
      ],
      "image": "ghcr.io/stacklok/dockyard/uvx/adb-mysql-mcp-server:1.0.0",
      "permissions": {
        "network": {
          "outbound": {
            "insecure_allow_all": true
          }
        }
      },
      "env_vars": [
        {
          "name": "ADB_MYSQL_HOST",
          "description": "AnalyticDB for MySQL host address",
          "required": true
        },
        {
          "name": "ADB_MYSQL_PASSWORD",
          "description": "Database password for authentication",
          "required": true,
          "secret": true
        }
      ],
      "provenance": {
        "sigstore_url": "tuf-repo-cdn.sigstore.dev",
        "repository_uri": "https://github.com/stacklok/dockyard"
      }
    },
    "apollo-mcp-server": {
      "description": "Exposes GraphQL operations as MCP tools for AI-driven API orchestration with Apollo",
      "tier": "Official",
      "status": "Active",
      "transport": "streamable-http",
      "tools": [
        "example_GetAstronautsCurrentlyInSpace"
      ],
      "metadata": {
        "stars": 188,
        "pulls": 0,
        "last_updated": "2025-09-09T02:30:39Z"
      },
      "repository_url": "https://github.com/apollographql/apollo-mcp-server",
      "tags": [
        "graphql",
        "api",
        "orchestration",
        "apollo",
        "mcp"
      ],
      "image": "ghcr.io/apollographql/apollo-mcp-server:v0.7.5",
      "target_port": 5000,
      "permissions": {
        "network": {
          "outbound": {
            "insecure_allow_all": true,
            "allow_port": [
              443
            ]
          }
        }
      },
      "env_vars": [
        {
          "name": "APOLLO_GRAPH_REF",
          "description": "Graph ref (graph ID and variant) used to fetch persisted queries or schema",
          "required": false
        },
        {
          "name": "APOLLO_KEY",
          "description": "Apollo Studio API key for the graph",
          "required": false,
          "secret": true
        }
      ]
    },
    "arxiv-mcp-server": {
      "description": "AI assistants search and access arXiv papers through MCP with persistent paper storage",
      "tier": "Community",
      "status": "Active",
      "transport": "stdio",
      "tools": [
        "search_papers",
        "download_paper",
        "list_papers",
        "read_paper"
      ],
      "metadata": {
        "stars": 1619,
        "pulls": 77,
        "last_updated": "2025-08-27T02:30:22Z"
      },
      "repository_url": "https://github.com/blazickjp/arxiv-mcp-server",
      "tags": [
        "research",
        "academic",
        "papers",
        "arxiv",
        "search"
      ],
      "image": "ghcr.io/stacklok/dockyard/uvx/arxiv-mcp-server:0.3.0",
      "permissions": {
        "network": {
          "outbound": {
            "allow_host": [
              "arxiv.org",
              "export.arxiv.org"
            ],
            "allow_port": [
              443,
              80
            ]
          }
        }
      },
      "env_vars": [
        {
          "name": "ARXIV_STORAGE_PATH",
          "description": "Directory path for storing downloaded papers",
          "required": false,
          "default": "/arxiv-papers"
        }
      ],
      "args": [
        "--storage-path",
        "/arxiv-papers"
      ]
    }
  },
  "remote_servers": {
    "atlassian-remote": {
      "description": "Atlassian's official remote MCP server for Jira, Confluence, and Compass with OAuth 2.1",
      "tier": "Official",
      "status": "Active",
      "transport": "sse",
      "tools": [
        "atlassianUserInfo",
        "getAccessibleAtlassianResources",
        "getConfluenceSpaces",
        "getConfluencePage",
        "getJiraIssue",
        "createJiraIssue",
        "updateJiraIssue"
      ],
      "metadata": {
        "stars": 25,
        "pulls": 12,
        "last_updated": "2025-09-02T14:22:18Z"
      },
      "repository_url": "https://github.com/atlassian-labs/mcp-server",
      "tags": [
        "productivity",
        "jira",
        "confluence",
        "atlassian",
        "oauth"
      ],
      "url": "https://mcp.atlassian.com",
      "headers": [
        {
          "name": "Authorization",
          "description": "Bearer token for API authentication",
          "required": true,
          "secret": true
        }
      ],
      "oauth_config": {
        "issuer": "https://auth.atlassian.com",
        "scopes": ["read:jira-work", "write:jira-work", "read:confluence-content"],
        "use_pkce": true
      },
      "env_vars": [
        {
          "name": "ATLASSIAN_CLIENT_ID",
          "description": "OAuth client ID for Atlassian integration",
          "required": true,
          "secret": true
        }
      ]
    }
  }
}`

// realisticRegistryProvider implements RegistryDataProvider for testing with our realistic test data
type realisticRegistryProvider struct {
	data *registry.Registry
}

// newRealisticRegistryProvider creates a provider with our representative test data
func newRealisticRegistryProvider() (*realisticRegistryProvider, error) {
	var data registry.Registry
	if err := json.Unmarshal([]byte(testRegistryJSON), &data); err != nil {
		return nil, err
	}

	return &realisticRegistryProvider{
		data: &data,
	}, nil
}

// GetRegistryData implements RegistryDataProvider.GetRegistryData
func (p *realisticRegistryProvider) GetRegistryData(_ context.Context) (*registry.Registry, error) {
	return p.data, nil
}

// GetSource implements RegistryDataProvider.GetSource
func (*realisticRegistryProvider) GetSource() string {
	return "test:realistic-registry-data"
}

// GetRegistryName implements RegistryDataProvider.GetRegistryName
func (*realisticRegistryProvider) GetRegistryName() string {
	return "test-registry"
}

func TestHealthRouter(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)
	// Set up expectations for readiness check
	mockSvc.EXPECT().CheckReadiness(gomock.Any()).Return(nil).AnyTimes()

	router := v1.HealthRouter(mockSvc)

	tests := []struct {
		name       string
		path       string
		method     string
		wantStatus int
	}{
		{
			name:       "health endpoint",
			path:       "/health",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
		{
			name:       "readiness endpoint - ready",
			path:       "/readiness",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
		{
			name:       "version endpoint",
			path:       "/version",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(tt.method, tt.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

func TestRegistryRouter(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)
	// Set up expectations for all routes
	mockSvc.EXPECT().GetRegistry(gomock.Any()).Return(&registry.Registry{
		Version:     "1.0.0",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers:     make(map[string]*registry.ImageMetadata),
	}, "test", nil).AnyTimes()
	mockSvc.EXPECT().ListServers(gomock.Any()).Return([]registry.ServerMetadata{}, nil).AnyTimes()
	mockSvc.EXPECT().GetServer(gomock.Any(), "test-server").Return(&registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name: "test-server",
		},
	}, nil).AnyTimes()
	mockSvc.EXPECT().ListDeployedServers(gomock.Any()).Return([]*service.DeployedServer{}, nil).AnyTimes()
	mockSvc.EXPECT().GetDeployedServer(gomock.Any(), "test-server").Return([]*service.DeployedServer{
		{
			Name:      "test-server",
			Namespace: "default",
			Status:    "running",
		},
	}, nil).AnyTimes()

	router := v1.Router(mockSvc)

	tests := []struct {
		name       string
		path       string
		method     string
		wantStatus int
	}{
		{
			name:       "registry info",
			path:       "/info",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
		{
			name:       "list servers",
			path:       "/servers",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
		{
			name:       "get server",
			path:       "/servers/test-server",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
		{
			name:       "list deployed servers",
			path:       "/servers/deployed",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
		{
			name:       "get deployed server",
			path:       "/servers/deployed/test-server",
			method:     "GET",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(tt.method, tt.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

func TestListServers_FormatParameter(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)
	// Expect successful calls for toolhive format only
	mockSvc.EXPECT().ListServers(gomock.Any()).Return([]registry.ServerMetadata{}, nil).Times(2) // default and explicit toolhive

	router := v1.Router(mockSvc)

	tests := []struct {
		name       string
		format     string
		wantStatus int
	}{
		{
			name:       "default format (toolhive)",
			format:     "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "explicit toolhive format",
			format:     "toolhive",
			wantStatus: http.StatusOK,
		},
		{
			name:       "upstream format not implemented",
			format:     "upstream",
			wantStatus: http.StatusNotImplemented,
		},
		{
			name:       "invalid format not supported",
			format:     "invalid",
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := "/servers"
			if tt.format != "" {
				path += "?format=" + tt.format
			}

			req, err := http.NewRequest("GET", path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

func TestGetServer_NotFound(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSvc := mocks.NewMockRegistryService(ctrl)
	// Expect server not found error
	mockSvc.EXPECT().GetServer(gomock.Any(), "nonexistent").Return(nil, service.ErrServerNotFound)

	router := v1.Router(mockSvc)

	req, err := http.NewRequest("GET", "/servers/nonexistent", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestNewServer(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)

	// Set up expectations for all test routes
	mockSvc.EXPECT().CheckReadiness(gomock.Any()).Return(nil).AnyTimes()
	mockSvc.EXPECT().GetRegistry(gomock.Any()).Return(&registry.Registry{
		Version:     "1.0.0",
		LastUpdated: time.Now().Format(time.RFC3339),
		Servers:     make(map[string]*registry.ImageMetadata),
	}, "test", nil).AnyTimes()
	mockSvc.EXPECT().ListServers(gomock.Any()).Return([]registry.ServerMetadata{}, nil).AnyTimes()
	mockSvc.EXPECT().GetServer(gomock.Any(), "test").Return(&registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name: "test",
		},
	}, nil).AnyTimes()
	mockSvc.EXPECT().ListDeployedServers(gomock.Any()).Return([]*service.DeployedServer{}, nil).AnyTimes()

	// Create server with mock service (no options needed for basic testing)
	router := v1.NewServer(mockSvc)
	require.NotNil(t, router)

	// Test that routes are registered
	testRoutes := []struct {
		path       string
		method     string
		wantStatus int
	}{
		{"/health", "GET", http.StatusOK},
		{"/readiness", "GET", http.StatusOK}, // Ready with mock service
		{"/version", "GET", http.StatusOK},
		{"/openapi.json", "GET", http.StatusNotImplemented},
		{"/v0/info", "GET", http.StatusOK},
		{"/v0/servers", "GET", http.StatusOK},
		{"/v0/servers/test", "GET", http.StatusOK},
		{"/v0/servers/deployed", "GET", http.StatusOK},
		{"/notfound", "GET", http.StatusNotFound},
	}

	for _, tt := range testRoutes {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(tt.method, tt.path, nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}

func TestNewServer_WithMockService(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSvc := mocks.NewMockRegistryService(ctrl)

	// Expect readiness check to succeed
	mockSvc.EXPECT().CheckReadiness(gomock.Any()).Return(nil)

	// Create server with mock service (no options needed)
	router := v1.NewServer(mockSvc)
	require.NotNil(t, router)

	// Test readiness with custom service
	req, err := http.NewRequest("GET", "/readiness", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code) // Should be ready with mock service
}

func TestNewServer_WithMiddleware(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockSvc := mocks.NewMockRegistryService(ctrl)

	// Expect readiness check to succeed
	mockSvc.EXPECT().CheckReadiness(gomock.Any()).Return(nil)

	// Test middleware that adds a custom header
	testMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test-Middleware", "applied")
			next.ServeHTTP(w, r)
		})
	}

	// Create server with middleware
	router := v1.NewServer(mockSvc, v1.WithMiddlewares(testMiddleware))
	require.NotNil(t, router)

	// Test that middleware is applied
	req, err := http.NewRequest("GET", "/readiness", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "applied", rr.Header().Get("X-Test-Middleware"))
}

// fileBasedRegistryProvider implements RegistryDataProvider for testing with embedded registry data
type fileBasedRegistryProvider struct {
	data *registry.Registry
}

// newFileBasedRegistryProvider creates a new provider with embedded registry data
func newFileBasedRegistryProvider() (*fileBasedRegistryProvider, error) {
	var data registry.Registry
	if err := json.Unmarshal([]byte(testRegistryJSON), &data); err != nil {
		return nil, err
	}

	return &fileBasedRegistryProvider{
		data: &data,
	}, nil
}

// GetRegistryData implements RegistryDataProvider.GetRegistryData
func (p *fileBasedRegistryProvider) GetRegistryData(_ context.Context) (*registry.Registry, error) {
	return p.data, nil
}

// GetSource implements RegistryDataProvider.GetSource
func (*fileBasedRegistryProvider) GetSource() string {
	return "embedded:pkg/registry/data/registry.json"
}

// GetRegistryName implements RegistryDataProvider.GetRegistryName
func (*fileBasedRegistryProvider) GetRegistryName() string {
	return "embedded-registry"
}

// Helper functions for testing the response conversion functions
func newServerSummaryResponseForTesting(server registry.ServerMetadata) v1.ServerSummaryResponse {
	return v1.NewServerSummaryResponseForTesting(server)
}

func newServerDetailResponseForTesting(server registry.ServerMetadata) v1.ServerDetailResponse {
	return v1.NewServerDetailResponseForTesting(server)
}

// TestRoutesWithRealData tests all routes using the embedded registry.json data
// This provides integration-style testing with realistic MCP server configurations
func TestRoutesWithRealData(t *testing.T) {
	t.Parallel()
	// Create the file-based provider with embedded data
	provider, err := newFileBasedRegistryProvider()
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Create a real service instance with the provider
	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)
	require.NotNil(t, svc)

	// Create router with the real service
	router := v1.Router(svc)
	require.NotNil(t, router)

	// Test registry info endpoint
	t.Run("registry info with real data", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/info", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Verify response structure
		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Validate key fields exist
		assert.Contains(t, response, "version")
		assert.Contains(t, response, "last_updated")
		assert.Contains(t, response, "total_servers")
		assert.Contains(t, response, "source")

		// Verify realistic data
		assert.Equal(t, "1.0.0", response["version"])
		assert.Equal(t, "embedded:pkg/registry/data/registry.json", response["source"])
		serverCount, ok := response["total_servers"].(float64)
		require.True(t, ok, "total_servers should be a number")
		assert.Greater(t, int(serverCount), 0, "should have servers from real data")
	})

	// Test list servers endpoint with different formats
	t.Run("list servers - toolhive format", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers?format=toolhive", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse response
		var response struct {
			Servers []map[string]interface{} `json:"servers"`
			Total   int                      `json:"total"`
		}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Greater(t, response.Total, 0, "should have servers from real data")
		assert.Greater(t, len(response.Servers), 0, "should have servers from real data")

		// Verify first server has expected ToolHive format fields
		if len(response.Servers) > 0 {
			server := response.Servers[0]
			assert.Contains(t, server, "name")
			assert.Contains(t, server, "description")
			assert.Contains(t, server, "tier")
			assert.Contains(t, server, "status")
			assert.Contains(t, server, "transport")
			assert.Contains(t, server, "tools_count")
		}
	})

	t.Run("list servers - upstream format not implemented", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers?format=upstream", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusNotImplemented, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse error response
		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify error response structure
		assert.Contains(t, response, "error")
		assert.Equal(t, "Upstream format not yet implemented", response["error"])
	})

	// Test get specific server with realistic data
	t.Run("get specific server", func(t *testing.T) {
		t.Parallel()
		// Use a known server from the registry data - let's use the first one we can find
		regData, err := provider.GetRegistryData(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, regData.Servers, "should have servers in registry data")

		// Get first server name
		var firstServerName string
		for name := range regData.Servers {
			firstServerName = name
			break
		}
		require.NotEmpty(t, firstServerName)

		req, err := http.NewRequest("GET", "/servers/"+firstServerName, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse and validate response
		var server map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &server)
		require.NoError(t, err)

		// Verify server details match expected data
		originalServer := regData.Servers[firstServerName]
		assert.Equal(t, originalServer.Description, server["description"])
		assert.Equal(t, originalServer.Tier, server["tier"])
		assert.Equal(t, originalServer.Status, server["status"])
		assert.Equal(t, originalServer.Transport, server["transport"])
	})

	// Test server not found
	t.Run("get nonexistent server", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers/nonexistent-server-12345", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
}

// TestFormatConversion tests the conversion between ToolHive and Upstream formats using real data
func TestFormatConversion(t *testing.T) {
	t.Parallel()
	// Create the file-based provider with embedded data
	provider, err := newFileBasedRegistryProvider()
	require.NoError(t, err)

	// Create service
	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)

	// Create router
	router := v1.Router(svc)

	// Get servers in ToolHive format
	req, err := http.NewRequest("GET", "/servers?format=toolhive", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var toolhiveResponse struct {
		Servers []map[string]interface{} `json:"servers"`
		Total   int                      `json:"total"`
	}
	err = json.Unmarshal(rr.Body.Bytes(), &toolhiveResponse)
	require.NoError(t, err)

	// Test upstream format returns not implemented
	req, err = http.NewRequest("GET", "/servers?format=upstream", nil)
	require.NoError(t, err)

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotImplemented, rr.Code)
	var upstreamResponse map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &upstreamResponse)
	require.NoError(t, err)

	// Verify error response
	assert.Contains(t, upstreamResponse, "error")
	assert.Equal(t, "Upstream format not yet implemented", upstreamResponse["error"])
}

// TestComplexServerConfiguration tests servers with complex configurations from real data
func TestComplexServerConfiguration(t *testing.T) {
	t.Parallel()
	provider, err := newFileBasedRegistryProvider()
	require.NoError(t, err)

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)

	router := v1.Router(svc)

	// Get registry data to find servers with complex configurations
	regData, err := provider.GetRegistryData(ctx)
	require.NoError(t, err)

	// Test servers with environment variables
	t.Run("servers with environment variables", func(t *testing.T) {
		t.Parallel()
		for serverName, serverData := range regData.Servers {
			if len(serverData.EnvVars) > 0 {
				req, err := http.NewRequest("GET", "/servers/"+serverName, nil)
				require.NoError(t, err)

				rr := httptest.NewRecorder()
				router.ServeHTTP(rr, req)

				assert.Equal(t, http.StatusOK, rr.Code)

				var response map[string]interface{}
				err = json.Unmarshal(rr.Body.Bytes(), &response)
				require.NoError(t, err)

				// Verify env_vars field exists and has content
				envVars, exists := response["env_vars"]
				if exists {
					envVarsList, ok := envVars.([]interface{})
					assert.True(t, ok, "env_vars should be an array")
					assert.Greater(t, len(envVarsList), 0, "should have env vars")
				}
				break // Test first server with env vars
			}
		}
	})
}

// TestRoutesWithRealisticData tests all routes using the curated realistic test data
// This provides focused integration-style testing with representative MCP server configurations
func TestRoutesWithRealisticData(t *testing.T) {
	t.Parallel()
	// Create the realistic provider with curated test data
	provider, err := newRealisticRegistryProvider()
	require.NoError(t, err)
	require.NotNil(t, provider)

	// Create a real service instance with the provider
	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)
	require.NotNil(t, svc)

	// Create router with the real service
	router := v1.Router(svc)
	require.NotNil(t, router)

	// Test registry info endpoint with realistic data
	t.Run("registry info with realistic data", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/info", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Verify response structure
		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Validate key fields exist
		assert.Contains(t, response, "version")
		assert.Contains(t, response, "last_updated")
		assert.Contains(t, response, "total_servers")
		assert.Contains(t, response, "source")

		// Verify realistic data
		assert.Equal(t, "1.0.0", response["version"])
		assert.Equal(t, "test:realistic-registry-data", response["source"])
		serverCount, ok := response["total_servers"].(float64)
		require.True(t, ok, "total_servers should be a number")
		assert.Equal(t, 4, int(serverCount), "should have 4 servers (3 container + 1 remote)")
	})

	// Test list servers endpoint - toolhive format with realistic data
	t.Run("list servers - toolhive format with realistic data", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers?format=toolhive", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse response
		var response struct {
			Servers []map[string]interface{} `json:"servers"`
			Total   int                      `json:"total"`
		}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Equal(t, 4, response.Total, "should have 4 total servers")
		assert.Len(t, response.Servers, 4, "should return 4 servers")

		// Verify server names are present and correct
		serverNames := make(map[string]bool)
		for _, server := range response.Servers {
			name, ok := server["name"].(string)
			require.True(t, ok, "server should have name field")
			serverNames[name] = true
		}

		expectedServers := []string{"adb-mysql-mcp-server", "apollo-mcp-server", "arxiv-mcp-server", "atlassian-remote"}
		for _, expected := range expectedServers {
			assert.True(t, serverNames[expected], "should contain server: %s", expected)
		}

		// Verify first server has expected ToolHive format fields
		if len(response.Servers) > 0 {
			server := response.Servers[0]
			assert.Contains(t, server, "name")
			assert.Contains(t, server, "description")
			assert.Contains(t, server, "tier")
			assert.Contains(t, server, "status")
			assert.Contains(t, server, "transport")
			assert.Contains(t, server, "tools_count")
		}
	})

	// Test list servers endpoint - upstream format not implemented with realistic data
	t.Run("list servers - upstream format not implemented with realistic data", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers?format=upstream", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusNotImplemented, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

		// Parse error response
		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify error response structure
		assert.Contains(t, response, "error")
		assert.Equal(t, "Upstream format not yet implemented", response["error"])
	})
}

// TestSpecificServersWithRealisticData tests individual server endpoints with our curated realistic data
func TestSpecificServersWithRealisticData(t *testing.T) {
	t.Parallel()
	provider, err := newRealisticRegistryProvider()
	require.NoError(t, err)

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)

	router := v1.Router(svc)

	testCases := []struct {
		name         string
		serverName   string
		expectedData map[string]interface{}
	}{
		{
			name:       "stdio server with complex config",
			serverName: "adb-mysql-mcp-server",
			expectedData: map[string]interface{}{
				"tier":        "Official",
				"transport":   "stdio",
				"status":      "Active",
				"description": "Official MCP server for AnalyticDB for MySQL of Alibaba Cloud",
			},
		},
		{
			name:       "streamable-http server",
			serverName: "apollo-mcp-server",
			expectedData: map[string]interface{}{
				"tier":      "Official",
				"transport": "streamable-http",
				"status":    "Active",
			},
		},
		{
			name:       "community server with args",
			serverName: "arxiv-mcp-server",
			expectedData: map[string]interface{}{
				"tier":      "Community",
				"transport": "stdio",
				"status":    "Active",
			},
		},
		{
			name:       "remote server with oauth",
			serverName: "atlassian-remote",
			expectedData: map[string]interface{}{
				"tier":      "Official",
				"transport": "sse",
				"status":    "Active",
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Test with toolhive format to get direct field access
			req, err := http.NewRequest("GET", "/servers/"+tt.serverName+"?format=toolhive", nil)
			require.NoError(t, err)

			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

			// Parse and validate response
			var server map[string]interface{}
			err = json.Unmarshal(rr.Body.Bytes(), &server)
			require.NoError(t, err)

			// Verify expected data
			for key, expectedValue := range tt.expectedData {
				actualValue, exists := server[key]
				assert.True(t, exists, "server should have field: %s", key)
				assert.Equal(t, expectedValue, actualValue, "field %s should match expected value", key)
			}
		})
	}
}

// TestFormatConversionWithRealisticData tests conversion between ToolHive and Upstream formats
// using realistic data to ensure the conversion pipeline works correctly
func TestFormatConversionWithRealisticData(t *testing.T) {
	t.Parallel()
	provider, err := newRealisticRegistryProvider()
	require.NoError(t, err)

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)

	router := v1.Router(svc)

	// Get servers in ToolHive format
	req, err := http.NewRequest("GET", "/servers?format=toolhive", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	var toolhiveResponse struct {
		Servers []map[string]interface{} `json:"servers"`
		Total   int                      `json:"total"`
	}
	err = json.Unmarshal(rr.Body.Bytes(), &toolhiveResponse)
	require.NoError(t, err)

	// Test that upstream format returns not implemented
	req, err = http.NewRequest("GET", "/servers?format=upstream", nil)
	require.NoError(t, err)

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotImplemented, rr.Code)
	var upstreamResponse map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &upstreamResponse)
	require.NoError(t, err)

	// Verify error response
	assert.Contains(t, upstreamResponse, "error")
	assert.Equal(t, "Upstream format not yet implemented", upstreamResponse["error"])
}

// BenchmarkRoutesWithRealisticData benchmarks the API endpoints using realistic test data
// This helps ensure performance doesn't regress with representative payloads
func BenchmarkRoutesWithRealisticData(b *testing.B) {
	provider, err := newRealisticRegistryProvider()
	if err != nil {
		b.Fatalf("Failed to create realistic provider: %v", err)
	}

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	if err != nil {
		b.Fatalf("Failed to create service: %v", err)
	}

	router := v1.Router(svc)

	b.Run("registry info", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/info", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})

	b.Run("list servers toolhive format", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/servers?format=toolhive", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})

	b.Run("list servers upstream format", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/servers?format=upstream", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})

	b.Run("get specific server", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/servers/apollo-mcp-server", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})
}

// BenchmarkRoutesWithRealData benchmarks the API endpoints using real data
// This helps ensure performance doesn't regress with realistic payloads
func BenchmarkRoutesWithRealData(b *testing.B) {
	provider, err := newFileBasedRegistryProvider()
	if err != nil {
		b.Fatalf("Failed to create provider: %v", err)
	}

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	if err != nil {
		b.Fatalf("Failed to create service: %v", err)
	}

	router := v1.Router(svc)

	b.Run("registry info", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/info", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})

	b.Run("list servers toolhive format", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/servers?format=toolhive", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})

	b.Run("list servers upstream format", func(b *testing.B) {
		req, _ := http.NewRequest("GET", "/servers?format=upstream", nil)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
		}
	})
}

// TestServerResponseStructures tests the new response structure changes
func TestServerResponseStructures(t *testing.T) {
	t.Parallel()

	provider, err := newRealisticRegistryProvider()
	require.NoError(t, err)

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)

	router := v1.Router(svc)

	t.Run("list servers returns tools_count instead of tools array", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		var response struct {
			Servers []struct {
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Tier        string   `json:"tier"`
				Status      string   `json:"status"`
				Transport   string   `json:"transport"`
				ToolsCount  int      `json:"tools_count"`
				Tools       []string `json:"tools,omitempty"` // Should not be present
			} `json:"servers"`
			Total int `json:"total"`
		}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Greater(t, len(response.Servers), 0)

		// Verify that we have tools_count and not tools array
		for _, server := range response.Servers {
			assert.Greater(t, server.ToolsCount, 0, "server %s should have tools_count > 0", server.Name)
			assert.Nil(t, server.Tools, "server %s should not have tools array in summary", server.Name)
		}
	})

	t.Run("get server details returns full tools array and image field", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers/apollo-mcp-server", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		var response struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Tier        string   `json:"tier"`
			Status      string   `json:"status"`
			Transport   string   `json:"transport"`
			Tools       []string `json:"tools"`
			Image       string   `json:"image"`
			ToolsCount  int      `json:"tools_count,omitempty"` // Should not be present
		}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Verify detailed response has full tools array and image
		assert.Greater(t, len(response.Tools), 0, "should have tools array in detailed response")
		assert.NotEmpty(t, response.Image, "should have image field")
		assert.Equal(t, 0, response.ToolsCount, "should not have tools_count in detailed response")

		// Verify specific fields
		assert.Equal(t, "apollo-mcp-server", response.Name)
		assert.Equal(t, "streamable-http", response.Transport)
		assert.Contains(t, response.Image, "apollo-mcp-server")
	})
}

// TestResponseFieldMapping tests the mapping between different response types
func TestResponseFieldMapping(t *testing.T) {
	t.Parallel()

	provider, err := newRealisticRegistryProvider()
	require.NoError(t, err)

	ctx := context.Background()
	svc, err := service.NewService(ctx, provider, nil)
	require.NoError(t, err)

	router := v1.Router(svc)

	t.Run("container server with env vars and permissions", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers/adb-mysql-mcp-server", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Check that env_vars are properly structured
		envVars, exists := response["env_vars"]
		assert.True(t, exists, "should have env_vars field")

		envVarsList, ok := envVars.([]interface{})
		assert.True(t, ok, "env_vars should be an array")
		assert.Greater(t, len(envVarsList), 0, "should have environment variables")

		// Check first env var structure
		if len(envVarsList) > 0 {
			envVar, ok := envVarsList[0].(map[string]interface{})
			assert.True(t, ok, "env var should be an object")

			assert.Contains(t, envVar, "name", "env var should have name")
			assert.Contains(t, envVar, "description", "env var should have description")
			assert.Contains(t, envVar, "required", "env var should have required field")
		}

		// Check permissions
		_, exists = response["permissions"]
		assert.True(t, exists, "should have permissions field")

		// Check image field
		image, exists := response["image"]
		assert.True(t, exists, "should have image field")
		assert.NotEmpty(t, image, "image field should not be empty")
	})

	t.Run("remote server fields", func(t *testing.T) {
		t.Parallel()
		req, err := http.NewRequest("GET", "/servers/atlassian-remote", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusOK, rr.Code)

		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)

		// Remote servers should have metadata with URL info
		metadata, exists := response["metadata"]
		assert.True(t, exists, "remote server should have metadata")

		metadataMap, ok := metadata.(map[string]interface{})
		assert.True(t, ok, "metadata should be an object")

		// Check remote-specific metadata fields
		assert.Contains(t, metadataMap, "url", "remote server metadata should contain URL")
		assert.Contains(t, metadataMap, "oauth_enabled", "remote server metadata should contain oauth_enabled")

		// Should not have image field for remote servers
		_, hasImage := response["image"]
		assert.False(t, hasImage, "remote server should not have image field")
	})
}

// TestHelperFunctions tests the individual helper functions
func TestHelperFunctions(t *testing.T) {
	t.Parallel()

	// Test data setup
	testImageMetadata := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        "test-server",
			Description: "Test server",
			Tier:        "Community",
			Status:      "Active",
			Transport:   "stdio",
			Tools:       []string{"tool1", "tool2", "tool3"},
		},
		Image: "test-image:latest",
	}

	testRemoteMetadata := &registry.RemoteServerMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:        "remote-server",
			Description: "Remote test server",
			Tier:        "Official",
			Status:      "Active",
			Transport:   "sse",
			Tools:       []string{"remote1", "remote2"},
		},
		URL: "https://remote.example.com",
	}

	t.Run("newServerSummaryResponse", func(t *testing.T) {
		t.Parallel()
		summary := newServerSummaryResponseForTesting(testImageMetadata)

		assert.Equal(t, "test-server", summary.Name)
		assert.Equal(t, "Test server", summary.Description)
		assert.Equal(t, "Community", summary.Tier)
		assert.Equal(t, "Active", summary.Status)
		assert.Equal(t, "stdio", summary.Transport)
		assert.Equal(t, 3, summary.ToolsCount)
	})

	t.Run("newServerDetailResponse with container server", func(t *testing.T) {
		t.Parallel()
		detail := newServerDetailResponseForTesting(testImageMetadata)

		assert.Equal(t, "test-server", detail.Name)
		assert.Equal(t, "Test server", detail.Description)
		assert.Equal(t, "Community", detail.Tier)
		assert.Equal(t, "Active", detail.Status)
		assert.Equal(t, "stdio", detail.Transport)
		assert.Equal(t, []string{"tool1", "tool2", "tool3"}, detail.Tools)
		assert.Equal(t, "test-image:latest", detail.Image)
	})

	t.Run("newServerDetailResponse with remote server", func(t *testing.T) {
		t.Parallel()
		detail := newServerDetailResponseForTesting(testRemoteMetadata)

		assert.Equal(t, "remote-server", detail.Name)
		assert.Equal(t, "Remote test server", detail.Description)
		assert.Equal(t, "Official", detail.Tier)
		assert.Equal(t, "Active", detail.Status)
		assert.Equal(t, "sse", detail.Transport)
		assert.Equal(t, []string{"remote1", "remote2"}, detail.Tools)
		assert.Empty(t, detail.Image, "remote server should not have image")

		// Check metadata contains URL info
		assert.Contains(t, detail.Metadata, "url")
		assert.Equal(t, "https://remote.example.com", detail.Metadata["url"])
	})
}

// TestErrorScenarios tests various error conditions
func TestErrorScenarios(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)

	t.Run("registry info service error", func(t *testing.T) {
		t.Parallel()
		mockSvc.EXPECT().GetRegistry(gomock.Any()).Return(nil, "", assert.AnError)

		router := v1.Router(mockSvc)
		req, err := http.NewRequest("GET", "/info", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)

		var response map[string]interface{}
		err = json.Unmarshal(rr.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Contains(t, response, "error")
	})

	t.Run("list servers service error", func(t *testing.T) {
		t.Parallel()
		mockSvc.EXPECT().ListServers(gomock.Any()).Return(nil, assert.AnError)

		router := v1.Router(mockSvc)
		req, err := http.NewRequest("GET", "/servers", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("get server service error", func(t *testing.T) {
		t.Parallel()
		mockSvc.EXPECT().GetServer(gomock.Any(), "error-server").Return(nil, assert.AnError)

		router := v1.Router(mockSvc)
		req, err := http.NewRequest("GET", "/servers/error-server", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("empty server name", func(t *testing.T) {
		t.Parallel()
		// This test needs its own mock since it calls ListServers (chi routes /servers/ to list endpoint)
		emptyNameMockSvc := mocks.NewMockRegistryService(ctrl)
		emptyNameMockSvc.EXPECT().ListServers(gomock.Any()).Return([]registry.ServerMetadata{}, nil)

		router := v1.Router(emptyNameMockSvc)

		// Test with empty path parameter (though chi wouldn't normally route this)
		req, err := http.NewRequest("GET", "/servers/", nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		// Chi will route this to the list servers endpoint, not the individual server endpoint
		// So we expect either a valid response or method not allowed
		assert.True(t, rr.Code == http.StatusOK || rr.Code == http.StatusMethodNotAllowed || rr.Code == http.StatusNotFound)
	})
}

// TestOpenAPIEndpoint tests the OpenAPI YAML endpoint
func TestOpenAPIEndpoint(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)
	router := v1.Router(mockSvc)

	req, err := http.NewRequest("GET", "/openapi.yaml", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/x-yaml", rr.Header().Get("Content-Type"))
	assert.Greater(t, len(rr.Body.String()), 0, "OpenAPI YAML should not be empty")
}

// TestPublishServerEndpoint tests the publish endpoint
func TestPublishServerEndpoint(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)
	router := v1.Router(mockSvc)

	req, err := http.NewRequest("POST", "/publish", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotImplemented, rr.Code)

	var response map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Contains(t, response, "error")
	assert.Equal(t, "Publishing is not supported by this registry implementation", response["error"])
}

// TestReadinessWithServiceError tests readiness endpoint when service has errors
func TestReadinessWithServiceError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockSvc := mocks.NewMockRegistryService(ctrl)
	mockSvc.EXPECT().CheckReadiness(gomock.Any()).Return(assert.AnError)

	router := v1.HealthRouter(mockSvc)
	req, err := http.NewRequest("GET", "/readiness", nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var response map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &response)
	require.NoError(t, err)
	assert.Contains(t, response, "error")
}

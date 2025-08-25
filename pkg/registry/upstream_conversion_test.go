package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/permissions"
)

func TestConvertUpstreamToToolhive_DockerPackage(t *testing.T) {
	t.Parallel()
	upstream := &UpstreamServerDetail{
		Server: UpstreamServer{
			Name:        "io.modelcontextprotocol/filesystem",
			Description: "Node.js server implementing Model Context Protocol (MCP) for filesystem operations.",
			Status:      UpstreamServerStatusActive,
			Repository: &UpstreamRepository{
				URL:    "https://github.com/modelcontextprotocol/servers",
				Source: "github",
				ID:     "b94b5f7e-c7c6-d760-2c78-a5e9b8a5b8c9",
			},
			VersionDetail: UpstreamVersionDetail{
				Version: "1.0.2",
			},
			Packages: []UpstreamPackage{
				{
					RegistryName: "docker",
					Name:         "mcp/filesystem",
					Version:      "1.0.2",
					PackageArguments: []UpstreamArgument{
						{
							Type:      UpstreamArgumentTypePositional,
							ValueHint: "target_dir",
							Value:     "/project",
						},
					},
					EnvironmentVariables: []UpstreamKeyValueInput{
						{
							Name:        "LOG_LEVEL",
							Description: "Logging level (debug, info, warn, error)",
							Default:     "info",
						},
					},
				},
			},
		},
		XPublisher: &UpstreamPublisher{
			XDevToolhive: &ToolhivePublisherExtension{
				Tier:      "Official",
				Transport: "stdio",
				Tools:     []string{"read_file", "write_file", "list_directory"},
				Tags:      []string{"filesystem", "files"},
			},
		},
	}

	result, err := ConvertUpstreamToToolhive(upstream)
	require.NoError(t, err)
	require.NotNil(t, result)

	imageMetadata, ok := result.(*ImageMetadata)
	require.True(t, ok, "Expected ImageMetadata")

	assert.Equal(t, "io.modelcontextprotocol/filesystem", imageMetadata.GetName())
	assert.Equal(t, "Node.js server implementing Model Context Protocol (MCP) for filesystem operations.", imageMetadata.GetDescription())
	assert.Equal(t, "Official", imageMetadata.GetTier())
	assert.Equal(t, "active", imageMetadata.GetStatus())
	assert.Equal(t, "stdio", imageMetadata.GetTransport())
	assert.Equal(t, "mcp/filesystem:1.0.2", imageMetadata.Image)
	assert.Equal(t, []string{"read_file", "write_file", "list_directory"}, imageMetadata.GetTools())
	assert.Equal(t, []string{"filesystem", "files"}, imageMetadata.GetTags())
	assert.Equal(t, "https://github.com/modelcontextprotocol/servers", imageMetadata.GetRepositoryURL())
	assert.False(t, imageMetadata.IsRemote())

	// Check environment variables
	require.Len(t, imageMetadata.EnvVars, 1)
	assert.Equal(t, "LOG_LEVEL", imageMetadata.EnvVars[0].Name)
	assert.Equal(t, "Logging level (debug, info, warn, error)", imageMetadata.EnvVars[0].Description)
	assert.Equal(t, "info", imageMetadata.EnvVars[0].Default)
	assert.False(t, imageMetadata.EnvVars[0].Required)

	// Check arguments
	require.Len(t, imageMetadata.Args, 1)
	assert.Equal(t, "/project", imageMetadata.Args[0])
}

func TestConvertUpstreamToToolhive_NPMPackage(t *testing.T) {
	t.Parallel()
	upstream := &UpstreamServerDetail{
		Server: UpstreamServer{
			Name:        "io.modelcontextprotocol/brave-search",
			Description: "MCP server for Brave Search API integration",
			Status:      UpstreamServerStatusActive,
			Repository: &UpstreamRepository{
				URL:    "https://github.com/modelcontextprotocol/servers",
				Source: "github",
			},
			VersionDetail: UpstreamVersionDetail{
				Version: "1.0.2",
			},
			Packages: []UpstreamPackage{
				{
					RegistryName: "npm",
					Name:         "@modelcontextprotocol/server-brave-search",
					Version:      "1.0.2",
					EnvironmentVariables: []UpstreamKeyValueInput{
						{
							Name:        "BRAVE_API_KEY",
							Description: "Brave Search API Key",
							IsRequired:  true,
							IsSecret:    true,
						},
					},
				},
			},
		},
	}

	result, err := ConvertUpstreamToToolhive(upstream)
	require.NoError(t, err)
	require.NotNil(t, result)

	imageMetadata, ok := result.(*ImageMetadata)
	require.True(t, ok, "Expected ImageMetadata")

	assert.Equal(t, "io.modelcontextprotocol/brave-search", imageMetadata.GetName())
	assert.Equal(t, "MCP server for Brave Search API integration", imageMetadata.GetDescription())
	assert.Equal(t, "Community", imageMetadata.GetTier()) // Default value
	assert.Equal(t, "active", imageMetadata.GetStatus())
	assert.Equal(t, "stdio", imageMetadata.GetTransport())                                        // Default value
	assert.Equal(t, "npx://@modelcontextprotocol/server-brave-search@1.0.2", imageMetadata.Image) // NPM package as protocol scheme

	// Check environment variables
	require.Len(t, imageMetadata.EnvVars, 1)
	assert.Equal(t, "BRAVE_API_KEY", imageMetadata.EnvVars[0].Name)
	assert.Equal(t, "Brave Search API Key", imageMetadata.EnvVars[0].Description)
	assert.True(t, imageMetadata.EnvVars[0].Required)
	assert.True(t, imageMetadata.EnvVars[0].Secret)
}

func TestConvertUpstreamToToolhive_RemoteServer(t *testing.T) {
	t.Parallel()
	upstream := &UpstreamServerDetail{
		Server: UpstreamServer{
			Name:        "Remote Filesystem Server",
			Description: "Cloud-hosted MCP filesystem server",
			Repository: &UpstreamRepository{
				URL:    "https://github.com/example/remote-fs",
				Source: "github",
				ID:     "xyz789ab-cdef-0123-4567-890ghijklmno",
			},
			VersionDetail: UpstreamVersionDetail{
				Version: "2.0.0",
			},
			Remotes: []UpstreamRemote{
				{
					TransportType: UpstreamTransportTypeSSE,
					URL:           "https://mcp-fs.example.com/sse",
					Headers: []UpstreamKeyValueInput{
						{
							Name:        "X-API-Key",
							Description: "API key for authentication",
							IsRequired:  true,
							IsSecret:    true,
						},
					},
				},
			},
		},
		XPublisher: &UpstreamPublisher{
			XDevToolhive: &ToolhivePublisherExtension{
				Tier:      "Community",
				Transport: "sse",
				Tools:     []string{"remote_read", "remote_write"},
			},
		},
	}

	result, err := ConvertUpstreamToToolhive(upstream)
	require.NoError(t, err)
	require.NotNil(t, result)

	remoteMetadata, ok := result.(*RemoteServerMetadata)
	require.True(t, ok, "Expected RemoteServerMetadata")

	assert.Equal(t, "Remote Filesystem Server", remoteMetadata.GetName())
	assert.Equal(t, "Cloud-hosted MCP filesystem server", remoteMetadata.GetDescription())
	assert.Equal(t, "Community", remoteMetadata.GetTier())
	assert.Equal(t, "active", remoteMetadata.GetStatus())
	assert.Equal(t, "sse", remoteMetadata.GetTransport())
	assert.Equal(t, "https://mcp-fs.example.com/sse", remoteMetadata.URL)
	assert.Equal(t, []string{"remote_read", "remote_write"}, remoteMetadata.GetTools())
	assert.Equal(t, "https://github.com/example/remote-fs", remoteMetadata.GetRepositoryURL())
	assert.True(t, remoteMetadata.IsRemote())

	// Check headers
	require.Len(t, remoteMetadata.Headers, 1)
	assert.Equal(t, "X-API-Key", remoteMetadata.Headers[0].Name)
	assert.Equal(t, "API key for authentication", remoteMetadata.Headers[0].Description)
	assert.True(t, remoteMetadata.Headers[0].Required)
	assert.True(t, remoteMetadata.Headers[0].Secret)
}

func TestConvertToolhiveToUpstream_ImageMetadata(t *testing.T) {
	t.Parallel()
	imageMetadata := &ImageMetadata{
		BaseServerMetadata: BaseServerMetadata{
			Name:          "test-server",
			Description:   "Test MCP server",
			Tier:          "Official",
			Status:        "active",
			Transport:     "stdio",
			Tools:         []string{"test_tool1", "test_tool2"},
			RepositoryURL: "https://github.com/example/test-server",
			Tags:          []string{"test", "example"},
			CustomMetadata: map[string]any{
				"custom_field": "custom_value",
			},
		},
		Image:      "test/server:1.0.0",
		TargetPort: 8080,
		Permissions: &permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					AllowHost: []string{"example.com"},
				},
			},
		},
		EnvVars: []*EnvVar{
			{
				Name:        "TEST_VAR",
				Description: "Test environment variable",
				Required:    true,
				Secret:      false,
			},
		},
		Args:       []string{"--config", "/app/config.json"},
		DockerTags: []string{"1.0.0", "latest"},
	}

	result, err := ConvertToolhiveToUpstream(imageMetadata)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, "test-server", result.Server.Name)
	assert.Equal(t, "Test MCP server", result.Server.Description)
	assert.Equal(t, UpstreamServerStatusActive, result.Server.Status)
	assert.Equal(t, "1.0.0", result.Server.VersionDetail.Version)

	// Check repository
	require.NotNil(t, result.Server.Repository)
	assert.Equal(t, "https://github.com/example/test-server", result.Server.Repository.URL)
	assert.Equal(t, "github", result.Server.Repository.Source)

	// Check packages
	require.Len(t, result.Server.Packages, 1)
	pkg := result.Server.Packages[0]
	assert.Equal(t, "docker", pkg.RegistryName)
	assert.Equal(t, "test/server", pkg.Name)
	assert.Equal(t, "1.0.0", pkg.Version)

	// Check package arguments
	require.Len(t, pkg.PackageArguments, 2)
	assert.Equal(t, UpstreamArgumentTypePositional, pkg.PackageArguments[0].Type)
	assert.Equal(t, "--config", pkg.PackageArguments[0].Value)
	assert.Equal(t, UpstreamArgumentTypePositional, pkg.PackageArguments[1].Type)
	assert.Equal(t, "/app/config.json", pkg.PackageArguments[1].Value)

	// Check environment variables
	require.Len(t, pkg.EnvironmentVariables, 1)
	envVar := pkg.EnvironmentVariables[0]
	assert.Equal(t, "TEST_VAR", envVar.Name)
	assert.Equal(t, "Test environment variable", envVar.Description)
	assert.True(t, envVar.IsRequired)
	assert.False(t, envVar.IsSecret)

	// Check x-publisher
	require.NotNil(t, result.XPublisher)

	// Check toolhive extension
	require.NotNil(t, result.XPublisher.XDevToolhive)
	toolhiveExt := result.XPublisher.XDevToolhive
	assert.Equal(t, "Official", toolhiveExt.Tier)
	assert.Equal(t, "stdio", toolhiveExt.Transport)
	assert.Equal(t, []string{"test_tool1", "test_tool2"}, toolhiveExt.Tools)
	assert.Equal(t, []string{"test", "example"}, toolhiveExt.Tags)
	assert.Equal(t, 8080, toolhiveExt.TargetPort)
	assert.Equal(t, []string{"1.0.0", "latest"}, toolhiveExt.DockerTags)
	assert.NotNil(t, toolhiveExt.Permissions)
}

func TestConvertUpstreamToToolhive_NilInput(t *testing.T) {
	t.Parallel()
	result, err := ConvertUpstreamToToolhive(nil)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "upstream server detail is nil")
}

func TestConvertToolhiveToUpstream_NilInput(t *testing.T) {
	t.Parallel()
	result, err := ConvertToolhiveToUpstream(nil)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "server metadata is nil")
}

func TestConvertUpstreamToToolhive_NoPackagesOrRemotes(t *testing.T) {
	t.Parallel()
	upstream := &UpstreamServerDetail{
		Server: UpstreamServer{
			Name:        "empty-server",
			Description: "Server with no packages or remotes",
			VersionDetail: UpstreamVersionDetail{
				Version: "1.0.0",
			},
		},
	}

	result, err := ConvertUpstreamToToolhive(upstream)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "no packages found for container server")
}

func TestConvertStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    UpstreamServerStatus
		expected string
	}{
		{UpstreamServerStatusActive, "active"},
		{UpstreamServerStatusDeprecated, "deprecated"},
		{"", "active"}, // Default case
	}

	for _, test := range tests {
		result := convertStatus(test.input)
		assert.Equal(t, test.expected, result)
	}
}

func TestConvertTransport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    UpstreamTransportType
		expected string
	}{
		{UpstreamTransportTypeSSE, "sse"},
		{UpstreamTransportTypeStreamable, "streamable-http"},
		{"", "stdio"}, // Default case
	}

	for _, test := range tests {
		result := convertTransport(test.input)
		assert.Equal(t, test.expected, result)
	}
}

func TestFormatImageName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		pkg      *UpstreamPackage
		expected string
	}{
		{
			name: "Docker package",
			pkg: &UpstreamPackage{
				RegistryName: "docker",
				Name:         "nginx",
				Version:      "latest",
			},
			expected: "nginx:latest",
		},
		{
			name: "NPM package",
			pkg: &UpstreamPackage{
				RegistryName: "npm",
				Name:         "@modelcontextprotocol/server-filesystem",
				Version:      "1.0.2",
			},
			expected: "npx://@modelcontextprotocol/server-filesystem@1.0.2",
		},
		{
			name: "Python package",
			pkg: &UpstreamPackage{
				RegistryName: "pypi",
				Name:         "weather-mcp-server",
				Version:      "0.5.0",
			},
			expected: "uvx://weather-mcp-server@0.5.0",
		},
		{
			name: "Unknown registry",
			pkg: &UpstreamPackage{
				RegistryName: "unknown",
				Name:         "some-package",
				Version:      "1.0.0",
			},
			expected: "some-package",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result := formatImageName(test.pkg)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestExtractImageName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"nginx:latest", "nginx"},
		{"registry.example.com/myapp:v1.0.0", "registry.example.com/myapp"},
		{"simple-name", "simple-name"},
	}

	for _, test := range tests {
		result := extractImageName(test.input)
		assert.Equal(t, test.expected, result)
	}
}

func TestExtractImageTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"nginx:latest", "latest"},
		{"registry.example.com/myapp:v1.0.0", "v1.0.0"},
		{"simple-name", "latest"},
	}

	for _, test := range tests {
		result := extractImageTag(test.input)
		assert.Equal(t, test.expected, result)
	}
}

func TestExtractSourceFromURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"https://github.com/example/repo", "github"},
		{"https://gitlab.com/example/repo", "gitlab"},
		{"https://bitbucket.org/example/repo", "unknown"},
		{"https://example.com/repo", "unknown"},
	}

	for _, test := range tests {
		result := extractSourceFromURL(test.input)
		assert.Equal(t, test.expected, result)
	}
}

func TestConvertUpstreamToToolhive_PythonPackage(t *testing.T) {
	t.Parallel()
	upstream := &UpstreamServerDetail{
		Server: UpstreamServer{
			Name:        "weather-mcp-server",
			Description: "Python MCP server for weather data access",
			Repository: &UpstreamRepository{
				URL:    "https://github.com/example/weather-mcp",
				Source: "github",
			},
			VersionDetail: UpstreamVersionDetail{
				Version: "0.5.0",
			},
			Packages: []UpstreamPackage{
				{
					RegistryName: "pypi",
					Name:         "weather-mcp-server",
					Version:      "0.5.0",
					RuntimeHint:  "uvx",
					EnvironmentVariables: []UpstreamKeyValueInput{
						{
							Name:        "WEATHER_API_KEY",
							Description: "API key for weather service",
							IsRequired:  true,
							IsSecret:    true,
						},
						{
							Name:        "WEATHER_UNITS",
							Description: "Temperature units (celsius, fahrenheit)",
							Default:     "celsius",
						},
					},
				},
			},
		},
	}

	result, err := ConvertUpstreamToToolhive(upstream)
	require.NoError(t, err)
	require.NotNil(t, result)

	imageMetadata, ok := result.(*ImageMetadata)
	require.True(t, ok, "Expected ImageMetadata")

	assert.Equal(t, "weather-mcp-server", imageMetadata.GetName())
	assert.Equal(t, "Python MCP server for weather data access", imageMetadata.GetDescription())
	assert.Equal(t, "uvx://weather-mcp-server@0.5.0", imageMetadata.Image)

	// Check environment variables
	require.Len(t, imageMetadata.EnvVars, 2)

	// First env var
	assert.Equal(t, "WEATHER_API_KEY", imageMetadata.EnvVars[0].Name)
	assert.Equal(t, "API key for weather service", imageMetadata.EnvVars[0].Description)
	assert.True(t, imageMetadata.EnvVars[0].Required)
	assert.True(t, imageMetadata.EnvVars[0].Secret)

	// Second env var
	assert.Equal(t, "WEATHER_UNITS", imageMetadata.EnvVars[1].Name)
	assert.Equal(t, "Temperature units (celsius, fahrenheit)", imageMetadata.EnvVars[1].Description)
	assert.False(t, imageMetadata.EnvVars[1].Required)
	assert.False(t, imageMetadata.EnvVars[1].Secret)
	assert.Equal(t, "celsius", imageMetadata.EnvVars[1].Default)
}

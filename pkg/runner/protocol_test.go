package runner

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/container/templates"
)

func TestIsLocalGoPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "current directory",
			path:     ".",
			expected: true,
		},
		{
			name:     "relative path with ./",
			path:     "./cmd/server",
			expected: true,
		},
		{
			name:     "relative path with ../",
			path:     "../other-project",
			expected: true,
		},
		{
			name:     "absolute path",
			path:     "/home/user/project",
			expected: true,
		},
		{
			name:     "remote package",
			path:     "github.com/example/package",
			expected: false,
		},
		{
			name:     "remote package with version",
			path:     "github.com/example/package@v1.0.0",
			expected: false,
		},
		{
			name:     "simple package name",
			path:     "mypackage",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isLocalGoPath(tt.path)
			if result != tt.expected {
				t.Errorf("isLocalGoPath(%q) = %v, want %v", tt.path, result, tt.expected)
			}
		})
	}
}

func TestPackageNameToImageName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple package name",
			input:    "mypackage",
			expected: "mypackage",
		},
		{
			name:     "package with slashes",
			input:    "github.com/user/repo",
			expected: "github-com-user-repo",
		},
		{
			name:     "package with version",
			input:    "github.com/user/repo@v1.0.0",
			expected: "github-com-user-repo-v1-0-0",
		},
		{
			name:     "relative path with ./",
			input:    "./cmd/server",
			expected: "cmd-server",
		},
		{
			name:     "relative path with ../",
			input:    "../other-project",
			expected: "other-project",
		},
		{
			name:     "current directory",
			input:    ".",
			expected: "toolhive-container",
		},
		{
			name:     "path with dots",
			input:    "./my.project",
			expected: "my-project",
		},
		{
			name:     "complex path",
			input:    "./cmd/my.server/main",
			expected: "cmd-my-server-main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := PackageNameToImageName(tt.input)
			if result != tt.expected {
				t.Errorf("PackageNameToImageName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsImageProtocolScheme(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "uvx scheme",
			input:    "uvx://package-name",
			expected: true,
		},
		{
			name:     "npx scheme",
			input:    "npx://package-name",
			expected: true,
		},
		{
			name:     "go scheme",
			input:    "go://package-name",
			expected: true,
		},
		{
			name:     "go scheme with local path",
			input:    "go://./cmd/server",
			expected: true,
		},
		{
			name:     "regular image name",
			input:    "docker.io/library/alpine:latest",
			expected: false,
		},
		{
			name:     "registry server name",
			input:    "fetch",
			expected: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsImageProtocolScheme(tt.input)
			if result != tt.expected {
				t.Errorf("IsImageProtocolScheme(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTemplateDataWithLocalPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		packageName string
		expected    templates.TemplateData
	}{
		{
			name:        "remote package",
			packageName: "github.com/example/package",
			expected: templates.TemplateData{
				MCPPackage:  "github.com/example/package",
				MCPArgs:     []string{},
				IsLocalPath: false,
			},
		},
		{
			name:        "local relative path",
			packageName: "./cmd/server",
			expected: templates.TemplateData{
				MCPPackage:  "./cmd/server",
				MCPArgs:     []string{},
				IsLocalPath: true,
			},
		},
		{
			name:        "current directory",
			packageName: ".",
			expected: templates.TemplateData{
				MCPPackage:  ".",
				MCPArgs:     []string{},
				IsLocalPath: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Test the logic that would be used in HandleProtocolScheme
			isLocalPath := isLocalGoPath(tt.packageName)

			templateData := templates.TemplateData{
				MCPPackage:  tt.packageName,
				MCPArgs:     []string{},
				IsLocalPath: isLocalPath,
			}

			if templateData.MCPPackage != tt.expected.MCPPackage {
				t.Errorf("MCPPackage = %q, want %q", templateData.MCPPackage, tt.expected.MCPPackage)
			}
			if templateData.IsLocalPath != tt.expected.IsLocalPath {
				t.Errorf("IsLocalPath = %v, want %v", templateData.IsLocalPath, tt.expected.IsLocalPath)
			}
		})
	}
}

func TestCreateTemplateDataWithCmdArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		transportType   templates.TransportType
		packageName     string
		caCertPath      string
		cmdArgs         []string
		expectedPackage string
		expectedArgs    []string
		expectedLocal   bool
	}{
		{
			name:            "NPX package without args",
			transportType:   templates.TransportTypeNPX,
			packageName:     "@modelcontextprotocol/server-sequential-thinking",
			caCertPath:      "",
			cmdArgs:         []string{},
			expectedPackage: "@modelcontextprotocol/server-sequential-thinking",
			expectedArgs:    []string{},
			expectedLocal:   false,
		},
		{
			name:            "NPX package with single arg",
			transportType:   templates.TransportTypeNPX,
			packageName:     "@launchdarkly/mcp-server",
			caCertPath:      "",
			cmdArgs:         []string{"start"},
			expectedPackage: "@launchdarkly/mcp-server",
			expectedArgs:    []string{"start"},
			expectedLocal:   false,
		},
		{
			name:            "NPX package with multiple args",
			transportType:   templates.TransportTypeNPX,
			packageName:     "@upstash/context7-mcp@latest",
			caCertPath:      "",
			cmdArgs:         []string{"--transport", "faketransport"},
			expectedPackage: "@upstash/context7-mcp@latest",
			expectedArgs:    []string{"--transport", "faketransport"},
			expectedLocal:   false,
		},
		{
			name:            "UVX package with args",
			transportType:   templates.TransportTypeUVX,
			packageName:     "arxiv-mcp-server",
			caCertPath:      "",
			cmdArgs:         []string{"--verbose", "--debug"},
			expectedPackage: "arxiv-mcp-server",
			expectedArgs:    []string{"--verbose", "--debug"},
			expectedLocal:   false,
		},
		{
			name:            "Go package with args",
			transportType:   templates.TransportTypeGO,
			packageName:     "github.com/StacklokLabs/osv-mcp/cmd/server@latest",
			caCertPath:      "",
			cmdArgs:         []string{"--config", "/etc/config.yaml"},
			expectedPackage: "github.com/StacklokLabs/osv-mcp/cmd/server@latest",
			expectedArgs:    []string{"--config", "/etc/config.yaml"},
			expectedLocal:   false,
		},
		{
			name:            "Local Go path with args",
			transportType:   templates.TransportTypeGO,
			packageName:     "./cmd/server",
			caCertPath:      "",
			cmdArgs:         []string{"--port", "8080"},
			expectedPackage: "./cmd/server",
			expectedArgs:    []string{"--port", "8080"},
			expectedLocal:   true,
		},
		{
			name:            "Package with complex args including flags and values",
			transportType:   templates.TransportTypeNPX,
			packageName:     "@example/server",
			caCertPath:      "",
			cmdArgs:         []string{"--host", "0.0.0.0", "--port", "3000", "-v"},
			expectedPackage: "@example/server",
			expectedArgs:    []string{"--host", "0.0.0.0", "--port", "3000", "-v"},
			expectedLocal:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := createTemplateData(tt.transportType, tt.packageName, tt.caCertPath, tt.cmdArgs)
			if err != nil {
				t.Fatalf("createTemplateData() error = %v", err)
			}

			if result.MCPPackage != tt.expectedPackage {
				t.Errorf("MCPPackage = %q, want %q", result.MCPPackage, tt.expectedPackage)
			}

			if len(result.MCPArgs) != len(tt.expectedArgs) {
				t.Errorf("len(MCPArgs) = %d, want %d", len(result.MCPArgs), len(tt.expectedArgs))
			}

			for i, arg := range result.MCPArgs {
				if i >= len(tt.expectedArgs) {
					t.Errorf("unexpected arg at index %d: %q", i, arg)
					continue
				}
				if arg != tt.expectedArgs[i] {
					t.Errorf("MCPArgs[%d] = %q, want %q", i, arg, tt.expectedArgs[i])
				}
			}

			if result.IsLocalPath != tt.expectedLocal {
				t.Errorf("IsLocalPath = %v, want %v", result.IsLocalPath, tt.expectedLocal)
			}
		})
	}
}

func TestParseProtocolScheme(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		serverOrImage     string
		expectedTransport templates.TransportType
		expectedPackage   string
		expectError       bool
	}{
		{
			name:              "NPX scheme basic",
			serverOrImage:     "npx://package-name",
			expectedTransport: templates.TransportTypeNPX,
			expectedPackage:   "package-name",
			expectError:       false,
		},
		{
			name:              "NPX scheme with scoped package",
			serverOrImage:     "npx://@scope/package-name",
			expectedTransport: templates.TransportTypeNPX,
			expectedPackage:   "@scope/package-name",
			expectError:       false,
		},
		{
			name:              "NPX scheme with version",
			serverOrImage:     "npx://@scope/package-name@1.2.3",
			expectedTransport: templates.TransportTypeNPX,
			expectedPackage:   "@scope/package-name@1.2.3",
			expectError:       false,
		},
		{
			name:              "UVX scheme",
			serverOrImage:     "uvx://arxiv-mcp-server",
			expectedTransport: templates.TransportTypeUVX,
			expectedPackage:   "arxiv-mcp-server",
			expectError:       false,
		},
		{
			name:              "Go scheme",
			serverOrImage:     "go://github.com/example/package@latest",
			expectedTransport: templates.TransportTypeGO,
			expectedPackage:   "github.com/example/package@latest",
			expectError:       false,
		},
		{
			name:              "Go scheme with local path",
			serverOrImage:     "go://./cmd/server",
			expectedTransport: templates.TransportTypeGO,
			expectedPackage:   "./cmd/server",
			expectError:       false,
		},
		{
			name:          "Invalid scheme",
			serverOrImage: "invalid://package",
			expectError:   true,
		},
		{
			name:          "No scheme",
			serverOrImage: "docker.io/library/alpine:latest",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			transport, pkg, err := ParseProtocolScheme(tt.serverOrImage)

			if tt.expectError {
				if err == nil {
					t.Errorf("ParseProtocolScheme() expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseProtocolScheme() unexpected error = %v", err)
			}

			if transport != tt.expectedTransport {
				t.Errorf("transport = %q, want %q", transport, tt.expectedTransport)
			}

			if pkg != tt.expectedPackage {
				t.Errorf("package = %q, want %q", pkg, tt.expectedPackage)
			}
		})
	}
}

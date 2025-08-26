package runner

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/container/templates"
)

func TestParsePreRunConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		source         string
		fromConfigPath string
		expectedType   PreRunConfigType
		expectError    bool
	}{
		{
			name:           "Config file",
			source:         "",
			fromConfigPath: "/path/to/config.json",
			expectedType:   PreRunConfigTypeConfigFile,
			expectError:    false,
		},
		{
			name:         "Remote URL",
			source:       "https://example.com/mcp-server",
			expectedType: PreRunConfigTypeRemoteURL,
			expectError:  false,
		},
		{
			name:         "UVX protocol scheme",
			source:       "uvx://some-package",
			expectedType: PreRunConfigTypeProtocolScheme,
			expectError:  false,
		},
		{
			name:         "NPX protocol scheme",
			source:       "npx://@scope/package",
			expectedType: PreRunConfigTypeProtocolScheme,
			expectError:  false,
		},
		{
			name:         "Go protocol scheme",
			source:       "go://github.com/example/package",
			expectedType: PreRunConfigTypeProtocolScheme,
			expectError:  false,
		},
		{
			name:         "Go local path",
			source:       "go://./local-package",
			expectedType: PreRunConfigTypeProtocolScheme,
			expectError:  false,
		},
		{
			name:         "Container image",
			source:       "ghcr.io/example/mcp-server:latest",
			expectedType: PreRunConfigTypeContainerImage,
			expectError:  false,
		},
		{
			name:         "Simple image name",
			source:       "alpine:latest",
			expectedType: PreRunConfigTypeContainerImage,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			preConfig, err := ParsePreRunConfig(tt.source, tt.fromConfigPath)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if preConfig.Type != tt.expectedType {
				t.Errorf("Expected type %s, got %s", tt.expectedType, preConfig.Type)
			}

			if preConfig.Source != tt.source {
				t.Errorf("Expected source %s, got %s", tt.source, preConfig.Source)
			}
		})
	}
}

func TestProtocolSchemeSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		source                string
		expectedTransportType templates.TransportType
		expectedPackage       string
		expectedIsLocal       bool
	}{
		{
			name:                  "UVX package",
			source:                "uvx://some-package",
			expectedTransportType: templates.TransportTypeUVX,
			expectedPackage:       "some-package",
			expectedIsLocal:       false,
		},
		{
			name:                  "NPX scoped package",
			source:                "npx://@scope/package",
			expectedTransportType: templates.TransportTypeNPX,
			expectedPackage:       "@scope/package",
			expectedIsLocal:       false,
		},
		{
			name:                  "Go remote package",
			source:                "go://github.com/example/package",
			expectedTransportType: templates.TransportTypeGO,
			expectedPackage:       "github.com/example/package",
			expectedIsLocal:       false,
		},
		{
			name:                  "Go local path",
			source:                "go://./local-package",
			expectedTransportType: templates.TransportTypeGO,
			expectedPackage:       "./local-package",
			expectedIsLocal:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			preConfig, err := ParsePreRunConfig(tt.source, "")
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if preConfig.Type != PreRunConfigTypeProtocolScheme {
				t.Errorf("Expected type %s, got %s", PreRunConfigTypeProtocolScheme, preConfig.Type)
				return
			}

			src := preConfig.ParsedSource.(*ProtocolSchemeSource)
			if src.ProtocolTransportType != tt.expectedTransportType {
				t.Errorf("Expected transport type %s, got %s", tt.expectedTransportType, src.ProtocolTransportType)
			}

			if src.Package != tt.expectedPackage {
				t.Errorf("Expected package %s, got %s", tt.expectedPackage, src.Package)
			}

			if src.IsLocalPath != tt.expectedIsLocal {
				t.Errorf("Expected IsLocalPath %t, got %t", tt.expectedIsLocal, src.IsLocalPath)
			}
		})
	}
}

func TestContainerImageSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		source           string
		expectedRegistry string
		expectedName     string
		expectedTag      string
	}{
		{
			name:             "Full image reference",
			source:           "ghcr.io/example/mcp-server:v1.0.0",
			expectedRegistry: "ghcr.io",
			expectedName:     "example/mcp-server",
			expectedTag:      "v1.0.0",
		},
		{
			name:             "Docker Hub image",
			source:           "alpine:latest",
			expectedRegistry: "index.docker.io",
			expectedName:     "library/alpine",
			expectedTag:      "latest",
		},
		{
			name:             "Image without tag",
			source:           "ubuntu",
			expectedRegistry: "index.docker.io",
			expectedName:     "library/ubuntu",
			expectedTag:      "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			preConfig, err := ParsePreRunConfig(tt.source, "")
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if preConfig.Type != PreRunConfigTypeContainerImage {
				t.Errorf("Expected type %s, got %s", PreRunConfigTypeContainerImage, preConfig.Type)
				return
			}

			src := preConfig.ParsedSource.(*ContainerImageSource)
			if src.Registry != tt.expectedRegistry {
				t.Errorf("Expected registry %s, got %s", tt.expectedRegistry, src.Registry)
			}

			if src.Name != tt.expectedName {
				t.Errorf("Expected name %s, got %s", tt.expectedName, src.Name)
			}

			if src.Tag != tt.expectedTag {
				t.Errorf("Expected tag %s, got %s", tt.expectedTag, src.Tag)
			}
		})
	}
}

func TestPreRunConfigString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		source         string
		fromConfigPath string
		expectedString string
	}{
		{
			name:           "Config file",
			source:         "",
			fromConfigPath: "/path/to/config.json",
			expectedString: "Config file: /path/to/config.json",
		},
		{
			name:           "Remote URL",
			source:         "https://example.com/mcp-server",
			expectedString: "Remote URL: https://example.com/mcp-server",
		},
		{
			name:           "Protocol scheme",
			source:         "uvx://some-package",
			expectedString: "Protocol scheme uvx: some-package",
		},
		{
			name:           "Go local path",
			source:         "go://./local-package",
			expectedString: "Protocol scheme go (local): ./local-package",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			preConfig, err := ParsePreRunConfig(tt.source, tt.fromConfigPath)
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			result := preConfig.String()
			if result != tt.expectedString {
				t.Errorf("Expected string %q, got %q", tt.expectedString, result)
			}
		})
	}
}

func TestPreRunConfigHelperMethods(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		source         string
		expectedRemote bool
		expectedBuild  bool
	}{
		{
			name:           "Remote URL",
			source:         "https://example.com/mcp-server",
			expectedRemote: true,
			expectedBuild:  false,
		},
		{
			name:           "Protocol scheme",
			source:         "uvx://some-package",
			expectedRemote: false,
			expectedBuild:  true,
		},
		{
			name:           "Container image",
			source:         "alpine:latest",
			expectedRemote: false,
			expectedBuild:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			preConfig, err := ParsePreRunConfig(tt.source, "")
			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if preConfig.IsRemote() != tt.expectedRemote {
				t.Errorf("Expected IsRemote %t, got %t", tt.expectedRemote, preConfig.IsRemote())
			}

			if preConfig.RequiresBuild() != tt.expectedBuild {
				t.Errorf("Expected RequiresBuild %t, got %t", tt.expectedBuild, preConfig.RequiresBuild())
			}
		})
	}
}

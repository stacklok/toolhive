// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"strings"
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
				IsLocalPath: false,
				BuildArgs:   nil,
			},
		},
		{
			name:        "local relative path",
			packageName: "./cmd/server",
			expected: templates.TemplateData{
				MCPPackage:  "./cmd/server",
				IsLocalPath: true,
				BuildArgs:   nil,
			},
		},
		{
			name:        "current directory",
			packageName: ".",
			expected: templates.TemplateData{
				MCPPackage:  ".",
				IsLocalPath: true,
				BuildArgs:   nil,
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
				IsLocalPath: isLocalPath,
				BuildArgs:   nil,
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

func TestBuildFromProtocolSchemeWithNameDryRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		serverOrImage string
		caCertPath    string
		buildArgs     []string
		wantContains  []string
		wantErr       bool
	}{
		{
			name:          "NPX with buildArgs in dry-run",
			serverOrImage: "npx://@launchdarkly/mcp-server",
			buildArgs:     []string{"start"},
			wantContains: []string{
				`ENTRYPOINT ["npx", "@launchdarkly/mcp-server", "start"]`,
				"FROM node:22-alpine",
			},
			wantErr: false,
		},
		{
			name:          "UVX with multiple buildArgs in dry-run",
			serverOrImage: "uvx://example-package",
			buildArgs:     []string{"--transport", "stdio"},
			wantContains: []string{
				"example-package",
				"--transport",
				"stdio",
				"FROM python:3.13-slim",
			},
			wantErr: false,
		},
		{
			name:          "GO with buildArgs in dry-run",
			serverOrImage: "go://github.com/example/package",
			buildArgs:     []string{"serve"},
			wantContains: []string{
				`ENTRYPOINT ["/app/mcp-server", "serve"]`,
			},
			wantErr: false,
		},
		{
			name:          "NPX with buildArgs and invalid CA cert path",
			serverOrImage: "npx://@launchdarkly/mcp-server",
			caCertPath:    "/nonexistent/ca-cert.crt",
			buildArgs:     []string{"start"},
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			// Call BuildFromProtocolSchemeWithName with dry-run=true
			dockerfileContent, err := BuildFromProtocolSchemeWithName(
				ctx, nil, tt.serverOrImage, tt.caCertPath, "", tt.buildArgs, nil, true)

			if (err != nil) != tt.wantErr {
				t.Errorf("BuildFromProtocolSchemeWithName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				for _, want := range tt.wantContains {
					if !strings.Contains(dockerfileContent, want) {
						t.Errorf("Dockerfile does not contain expected string %q", want)
					}
				}
			}
		})
	}
}

func TestCreateTemplateData(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		transportType templates.TransportType
		packageName   string
		caCertPath    string
		buildArgs     []string
		expected      templates.TemplateData
		wantErr       bool
	}{
		{
			name:          "NPX with buildArgs",
			transportType: templates.TransportTypeNPX,
			packageName:   "@launchdarkly/mcp-server",
			caCertPath:    "",
			buildArgs:     []string{"start"},
			expected: templates.TemplateData{
				MCPPackage:  "@launchdarkly/mcp-server",
				IsLocalPath: false,
				BuildArgs:   []string{"start"},
			},
			wantErr: false,
		},
		{
			name:          "UVX with multiple buildArgs",
			transportType: templates.TransportTypeUVX,
			packageName:   "example-package",
			caCertPath:    "",
			buildArgs:     []string{"--transport", "stdio"},
			expected: templates.TemplateData{
				MCPPackage:  "example-package",
				IsLocalPath: false,
				BuildArgs:   []string{"--transport", "stdio"},
			},
			wantErr: false,
		},
		{
			name:          "GO with buildArgs",
			transportType: templates.TransportTypeGO,
			packageName:   "github.com/example/package",
			caCertPath:    "",
			buildArgs:     []string{"serve", "--verbose"},
			expected: templates.TemplateData{
				MCPPackage:  "github.com/example/package",
				IsLocalPath: false,
				BuildArgs:   []string{"serve", "--verbose"},
			},
			wantErr: false,
		},
		{
			name:          "GO local path with buildArgs",
			transportType: templates.TransportTypeGO,
			packageName:   "./cmd/server",
			caCertPath:    "",
			buildArgs:     []string{"--config", "config.yaml"},
			expected: templates.TemplateData{
				MCPPackage:  "./cmd/server",
				IsLocalPath: true,
				BuildArgs:   []string{"--config", "config.yaml"},
			},
			wantErr: false,
		},
		{
			name:          "NPX without buildArgs",
			transportType: templates.TransportTypeNPX,
			packageName:   "package-name",
			caCertPath:    "",
			buildArgs:     nil,
			expected: templates.TemplateData{
				MCPPackage:  "package-name",
				IsLocalPath: false,
				BuildArgs:   nil,
			},
			wantErr: false,
		},
		{
			name:          "buildArgs with single quote should fail",
			transportType: templates.TransportTypeUVX,
			packageName:   "example-package",
			caCertPath:    "",
			buildArgs:     []string{"--name", "test'arg"},
			expected:      templates.TemplateData{},
			wantErr:       true,
		},
		{
			name:          "buildArgs with other special characters should succeed",
			transportType: templates.TransportTypeNPX,
			packageName:   "example-package",
			caCertPath:    "",
			buildArgs:     []string{"--config", "file$with`special\"chars"},
			expected: templates.TemplateData{
				MCPPackage:  "example-package",
				IsLocalPath: false,
				BuildArgs:   []string{"--config", "file$with`special\"chars"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := createTemplateData(tt.transportType, tt.packageName, tt.caCertPath, tt.buildArgs, nil)

			if (err != nil) != tt.wantErr {
				t.Errorf("createTemplateData() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if result.MCPPackage != tt.expected.MCPPackage {
				t.Errorf("MCPPackage = %q, want %q", result.MCPPackage, tt.expected.MCPPackage)
			}
			if result.IsLocalPath != tt.expected.IsLocalPath {
				t.Errorf("IsLocalPath = %v, want %v", result.IsLocalPath, tt.expected.IsLocalPath)
			}
			if len(result.BuildArgs) != len(tt.expected.BuildArgs) {
				t.Errorf("BuildArgs length = %d, want %d", len(result.BuildArgs), len(tt.expected.BuildArgs))
			} else {
				for i, arg := range result.BuildArgs {
					if arg != tt.expected.BuildArgs[i] {
						t.Errorf("BuildArgs[%d] = %q, want %q", i, arg, tt.expected.BuildArgs[i])
					}
				}
			}
		})
	}
}

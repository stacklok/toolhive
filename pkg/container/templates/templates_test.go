// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package templates

import (
	"regexp"
	"strings"
	"testing"
)

func TestGetDockerfileTemplate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		transportType   TransportType
		data            TemplateData
		wantContains    []string
		wantMatches     []string // New field for regex patterns
		wantNotContains []string
		wantErr         bool
	}{
		{
			name:          "UVX transport",
			transportType: TransportTypeUVX,
			data: TemplateData{
				MCPPackage: "example-package",
			},
			wantContains: []string{
				"FROM python:",
				"apt-get install -y --no-install-recommends",
				"pip install --no-cache-dir uv",
				"package_spec=$(echo \"$package\" | sed 's/@/==/')",
				"uv tool install \"$package_spec\"",
				"COPY --from=builder --chown=appuser:appgroup /opt/uv-tools /opt/uv-tools",
				"ENTRYPOINT [\"sh\", \"-c\", \"exec 'example-package' \\\"$@\\\"\", \"--\"]",
			},
			wantMatches: []string{
				`FROM python:\d+\.\d+-slim AS builder`, // Match builder stage
				`FROM python:\d+\.\d+-slim`,            // Match runtime stage
			},
			wantNotContains: []string{
				"Add custom CA certificate",
				"update-ca-certificates",
			},
			wantErr: false,
		},
		{
			name:          "UVX transport with CA certificate",
			transportType: TransportTypeUVX,
			data: TemplateData{
				MCPPackage:    "example-package",
				CACertContent: "-----BEGIN CERTIFICATE-----\nMIICertificateContent\n-----END CERTIFICATE-----",
			},
			wantContains: []string{
				"FROM python:",
				"apt-get install -y --no-install-recommends",
				"pip install --no-cache-dir uv",
				"package_spec=$(echo \"$package\" | sed 's/@/==/')",
				"uv tool install \"$package_spec\"",
				"COPY --from=builder --chown=appuser:appgroup /opt/uv-tools /opt/uv-tools",
				"ENTRYPOINT [\"sh\", \"-c\", \"exec 'example-package' \\\"$@\\\"\", \"--\"]",
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM python:\d+\.\d+-slim AS builder`, // Match builder stage
				`FROM python:\d+\.\d+-slim`,            // Match runtime stage
			},
			wantNotContains: []string{},
			wantErr:         false,
		},
		{
			name:          "NPX transport",
			transportType: TransportTypeNPX,
			data: TemplateData{
				MCPPackage: "example-package",
			},
			wantContains: []string{
				"FROM node:",
				"npm install --save example-package",
				"COPY --from=builder --chown=appuser:appgroup /build/node_modules /app/node_modules",
				`ENTRYPOINT ["npx", "example-package"]`,
			},
			wantMatches: []string{
				`FROM node:\d+-alpine AS builder`, // Match builder stage
				`FROM node:\d+-alpine`,            // Match runtime stage
			},
			wantNotContains: []string{
				"Add custom CA certificate",
				"update-ca-certificates",
			},
			wantErr: false,
		},
		{
			name:          "NPX transport with CA certificate",
			transportType: TransportTypeNPX,
			data: TemplateData{
				MCPPackage:    "example-package",
				CACertContent: "-----BEGIN CERTIFICATE-----\nMIICertificateContent\n-----END CERTIFICATE-----",
			},
			wantContains: []string{
				"FROM node:",
				"npm install --save example-package",
				`ENTRYPOINT ["npx", "example-package"]`,
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM node:\d+-alpine AS builder`, // Match builder stage
				`FROM node:\d+-alpine`,            // Match runtime stage
			},
			wantNotContains: []string{},
			wantErr:         false,
		},
		{
			name:          "GO transport",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage: "example-package",
			},
			wantContains: []string{
				"FROM golang:",
				"if ! echo \"$package\" | grep -q '@'; then",
				"package=\"${package}@latest\"",
				"go install \"$package\"",
				"COPY --from=builder --chown=appuser:appgroup /app/mcp-server /app/mcp-server",
				"ENTRYPOINT [\"/app/mcp-server\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`,                          // Match builder stage
				`FROM index\.docker\.io/library/alpine:\d+\.\d+@sha256:[0-9a-f]+`, // Match runtime stage
			},
			wantNotContains: []string{
				"Add custom CA certificate",
				"update-ca-certificates",
			},
			wantErr: false,
		},
		{
			name:          "GO transport with CA certificate",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage:    "example-package",
				CACertContent: "-----BEGIN CERTIFICATE-----\nMIICertificateContent\n-----END CERTIFICATE-----",
			},
			wantContains: []string{
				"FROM golang:",
				"if ! echo \"$package\" | grep -q '@'; then",
				"package=\"${package}@latest\"",
				"go install \"$package\"",
				"ENTRYPOINT [\"/app/mcp-server\"]",
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`,                          // Match builder stage
				`FROM index\.docker\.io/library/alpine:\d+\.\d+@sha256:[0-9a-f]+`, // Match runtime stage
			},
			wantNotContains: []string{},
			wantErr:         false,
		},
		{
			name:          "GO transport with local path",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage:  "./cmd/server",
				IsLocalPath: true,
			},
			wantContains: []string{
				"FROM golang:",
				"COPY . /build/",
				"go build -o /app/mcp-server ./cmd/server",
				"COPY --from=builder --chown=appuser:appgroup /app/mcp-server /app/mcp-server",
				"COPY --from=builder --chown=appuser:appgroup /build/ /app/",
				"ENTRYPOINT [\"/app/mcp-server\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`,                          // Match builder stage
				`FROM index\.docker\.io/library/alpine:\d+\.\d+@sha256:[0-9a-f]+`, // Match runtime stage
			},
			wantNotContains: []string{
				"Add custom CA certificate",
			},
			wantErr: false,
		},
		{
			name:          "GO transport with local path - current directory",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage:  ".",
				IsLocalPath: true,
			},
			wantContains: []string{
				"FROM golang:",
				"COPY . /build/",
				"go build -o /app/mcp-server .",
				"COPY --from=builder --chown=appuser:appgroup /app/mcp-server /app/mcp-server",
				"ENTRYPOINT [\"/app/mcp-server\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`,                          // Match builder stage
				`FROM index\.docker\.io/library/alpine:\d+\.\d+@sha256:[0-9a-f]+`, // Match runtime stage
			},
			wantNotContains: []string{
				"Add custom CA certificate",
			},
			wantErr: false,
		},
		{
			name:          "NPX transport with BuildArgs",
			transportType: TransportTypeNPX,
			data: TemplateData{
				MCPPackage: "@launchdarkly/mcp-server",
				BuildArgs:  []string{"start"},
			},
			wantContains: []string{
				"FROM node:",
				"npm install --save @launchdarkly/mcp-server",
				"COPY --from=builder --chown=appuser:appgroup /build/node_modules /app/node_modules",
				`ENTRYPOINT ["npx", "@launchdarkly/mcp-server", "start"]`,
			},
			wantMatches: []string{
				`FROM node:\d+-alpine AS builder`,
				`FROM node:\d+-alpine`,
			},
			wantNotContains: nil,
			wantErr:         false,
		},
		{
			name:          "UVX transport with BuildArgs",
			transportType: TransportTypeUVX,
			data: TemplateData{
				MCPPackage: "example-package",
				BuildArgs:  []string{"--transport", "stdio"},
			},
			wantContains: []string{
				"FROM python:",
				"uv tool install \"$package_spec\"",
				"ENTRYPOINT [\"sh\", \"-c\", \"exec 'example-package' '--transport' 'stdio' \\\"$@\\\"\", \"--\"]",
			},
			wantMatches: []string{
				`FROM python:\d+\.\d+-slim AS builder`,
				`FROM python:\d+\.\d+-slim`,
			},
			wantNotContains: nil,
			wantErr:         false,
		},
		{
			name:          "GO transport with BuildArgs",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage: "example-package",
				BuildArgs:  []string{"serve", "--verbose"},
			},
			wantContains: []string{
				"FROM golang:",
				"go install \"$package\"",
				"ENTRYPOINT [\"/app/mcp-server\", \"serve\", \"--verbose\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`,
				`FROM index\.docker\.io/library/alpine:\d+\.\d+@sha256:[0-9a-f]+`,
			},
			wantNotContains: nil,
			wantErr:         false,
		},
		{
			name:          "Unsupported transport",
			transportType: "unsupported",
			data: TemplateData{
				MCPPackage: "example-package",
			},
			wantContains:    nil,
			wantNotContains: nil,
			wantErr:         true,
		},
		{
			name:          "NPX transport with BuildEnv",
			transportType: TransportTypeNPX,
			data: TemplateData{
				MCPPackage: "example-package",
				BuildEnv: map[string]string{
					"NPM_CONFIG_REGISTRY": "https://npm.corp.example.com",
				},
			},
			wantContains: []string{
				"FROM node:",
				"# Custom build environment variables",
				`ENV NPM_CONFIG_REGISTRY="https://npm.corp.example.com"`,
				"npm install --save example-package",
			},
			wantMatches: []string{
				`FROM node:\d+-alpine AS builder`,
			},
			wantNotContains: nil,
			wantErr:         false,
		},
		{
			name:          "UVX transport with BuildEnv",
			transportType: TransportTypeUVX,
			data: TemplateData{
				MCPPackage: "example-package",
				BuildEnv: map[string]string{
					"PIP_INDEX_URL":    "https://pypi.corp.example.com/simple",
					"UV_DEFAULT_INDEX": "https://pypi.corp.example.com/simple",
				},
			},
			wantContains: []string{
				"FROM python:",
				"# Custom build environment variables",
				`ENV PIP_INDEX_URL="https://pypi.corp.example.com/simple"`,
				`ENV UV_DEFAULT_INDEX="https://pypi.corp.example.com/simple"`,
				"uv tool install",
			},
			wantMatches: []string{
				`FROM python:\d+\.\d+-slim AS builder`,
			},
			wantNotContains: nil,
			wantErr:         false,
		},
		{
			name:          "GO transport with BuildEnv",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage: "example-package",
				BuildEnv: map[string]string{
					"GOPROXY":   "https://goproxy.corp.example.com",
					"GOPRIVATE": "github.com/myorg/*",
				},
			},
			wantContains: []string{
				"FROM golang:",
				"# Custom build environment variables",
				`ENV GOPROXY="https://goproxy.corp.example.com"`,
				`ENV GOPRIVATE="github.com/myorg/*"`,
				"go install",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`,
			},
			wantNotContains: nil,
			wantErr:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetDockerfileTemplate(tt.transportType, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDockerfileTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			// Check for exact string matches
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("GetDockerfileTemplate() = %v, want to contain %v", got, want)
				}
			}

			// Check for regex pattern matches
			for _, pattern := range tt.wantMatches {
				matched, err := regexp.MatchString(pattern, got)
				if err != nil {
					t.Errorf("Invalid regex pattern %v: %v", pattern, err)
					continue
				}
				if !matched {
					t.Errorf("GetDockerfileTemplate() = %v, want to match pattern %v", got, pattern)
				}
			}

			// Check for strings that should not be present
			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("GetDockerfileTemplate() = %v, want NOT to contain %v", got, notWant)
				}
			}
		})
	}
}

func TestRuntimeStageInstallsAdditionalPackages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType TransportType
		runtimeConfig *RuntimeConfig
		wantInRuntime string // string that must appear AFTER the second FROM
	}{
		{
			name:          "NPX runtime stage installs extra packages",
			transportType: TransportTypeNPX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "node:22-alpine",
				AdditionalPackages: []string{"git", "ca-certificates", "curl"},
			},
			wantInRuntime: "curl",
		},
		{
			name:          "UVX runtime stage installs extra packages",
			transportType: TransportTypeUVX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "python:3.13-slim",
				AdditionalPackages: []string{"ca-certificates", "git", "curl"},
			},
			wantInRuntime: "curl",
		},
		{
			name:          "GO runtime stage installs extra packages",
			transportType: TransportTypeGO,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "golang:1.25-alpine",
				AdditionalPackages: []string{"ca-certificates", "git", "curl"},
			},
			wantInRuntime: "curl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := TemplateData{
				MCPPackage:    "test-package",
				RuntimeConfig: tt.runtimeConfig,
			}

			result, err := GetDockerfileTemplate(tt.transportType, data)
			if err != nil {
				t.Fatalf("GetDockerfileTemplate() error = %v", err)
			}

			// Find the runtime stage (second FROM) and check that
			// AdditionalPackages appear there, not just in the builder.
			parts := strings.SplitN(result, "\nFROM ", 2)
			if len(parts) < 2 {
				t.Fatal("Dockerfile does not contain a second FROM (runtime stage)")
			}
			runtimeStage := parts[1]

			if !strings.Contains(runtimeStage, tt.wantInRuntime) {
				t.Errorf("runtime stage does not install %q.\nRuntime stage:\n%s", tt.wantInRuntime, runtimeStage)
			}
		})
	}
}

func TestEmptyAdditionalPackagesDoesNotBreakBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		transportType TransportType
		runtimeConfig *RuntimeConfig
	}{
		{
			name:          "NPX with empty packages",
			transportType: TransportTypeNPX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "node:22-alpine",
				AdditionalPackages: []string{},
			},
		},
		{
			name:          "GO with empty packages",
			transportType: TransportTypeGO,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "golang:1.25-alpine",
				AdditionalPackages: []string{},
			},
		},
		{
			name:          "UVX with empty packages",
			transportType: TransportTypeUVX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "python:3.13-slim",
				AdditionalPackages: []string{},
			},
		},
		{
			name:          "NPX with nil packages",
			transportType: TransportTypeNPX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "node:22-alpine",
				AdditionalPackages: nil,
			},
		},
		{
			name:          "GO with nil packages",
			transportType: TransportTypeGO,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "golang:1.25-alpine",
				AdditionalPackages: nil,
			},
		},
		{
			name:          "UVX with nil packages",
			transportType: TransportTypeUVX,
			runtimeConfig: &RuntimeConfig{
				BuilderImage:       "python:3.13-slim",
				AdditionalPackages: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := TemplateData{
				MCPPackage:    "test-package",
				RuntimeConfig: tt.runtimeConfig,
			}

			result, err := GetDockerfileTemplate(tt.transportType, data)
			if err != nil {
				t.Fatalf("GetDockerfileTemplate() error = %v", err)
			}

			// After "apk add --no-cache" or "apt-get install -y --no-install-recommends"
			// there must be at least one package name (a word starting with [a-z]).
			// If the next non-whitespace character isn't a letter, no packages were rendered.
			noPackageAfterApk := regexp.MustCompile(`apk add --no-cache\s*([^a-z]|$)`)
			noPackageAfterApt := regexp.MustCompile(`--no-install-recommends\s*([^a-z]|$)`)
			if noPackageAfterApk.MatchString(result) || noPackageAfterApt.MatchString(result) {
				t.Errorf("Dockerfile contains package install command with no packages.\nFull Dockerfile:\n%s", result)
			}
		})
	}
}

func TestParseTransportType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		s       string
		want    TransportType
		wantErr bool
	}{
		{
			name:    "UVX transport",
			s:       "uvx",
			want:    TransportTypeUVX,
			wantErr: false,
		},
		{
			name:    "NPX transport",
			s:       "npx",
			want:    TransportTypeNPX,
			wantErr: false,
		},
		{
			name:    "GO transport",
			s:       "go",
			want:    TransportTypeGO,
			wantErr: false,
		},
		{
			name:    "Unsupported transport",
			s:       "unsupported",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseTransportType(tt.s)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseTransportType() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseTransportType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStripVersionSuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "scoped package with version",
			input: "@launchdarkly/mcp-server@1.2.3",
			want:  "@launchdarkly/mcp-server",
		},
		{
			name:  "regular package with version",
			input: "example-package@1.0.0",
			want:  "example-package",
		},
		{
			name:  "scoped package without version",
			input: "@org/package",
			want:  "@org/package",
		},
		{
			name:  "regular package without version",
			input: "package",
			want:  "package",
		},
		{
			name:  "package with latest tag",
			input: "package@latest",
			want:  "package",
		},
		{
			name:  "scoped package with semver",
			input: "@scope/name@^1.2.3",
			want:  "@scope/name",
		},
		{
			name:  "package with prerelease version",
			input: "package@1.0.0-beta.1",
			want:  "package",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripVersionSuffix(tt.input)
			if got != tt.want {
				t.Errorf("stripVersionSuffix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

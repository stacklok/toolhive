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
				MCPArgs:    []string{"--arg1", "--arg2", "value"},
			},
			wantContains: []string{
				"FROM python:",
				"apt-get install -y --no-install-recommends",
				"pip install --no-cache-dir uv",
				"package_spec=$(echo \"$package\" | sed 's/@/==/')",
				"uv tool install \"$package_spec\"",
				"COPY --from=builder --chown=appuser:appgroup /opt/uv-tools /opt/uv-tools",
				"ENTRYPOINT [\"sh\", \"-c\", \"package='example-package'; exec \\\"${package%%@*}\\\" \\\"--arg1\\\" \\\"--arg2\\\" \\\"value\\\" \\\"$@\\\"\", \"--\"]",
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
				MCPArgs:       []string{"--arg1", "--arg2", "value"},
				CACertContent: "-----BEGIN CERTIFICATE-----\nMIICertificateContent\n-----END CERTIFICATE-----",
			},
			wantContains: []string{
				"FROM python:",
				"apt-get install -y --no-install-recommends",
				"pip install --no-cache-dir uv",
				"package_spec=$(echo \"$package\" | sed 's/@/==/')",
				"uv tool install \"$package_spec\"",
				"COPY --from=builder --chown=appuser:appgroup /opt/uv-tools /opt/uv-tools",
				"ENTRYPOINT [\"sh\", \"-c\", \"package='example-package'; exec \\\"${package%%@*}\\\" \\\"--arg1\\\" \\\"--arg2\\\" \\\"value\\\" \\\"$@\\\"\", \"--\"]",
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
				MCPArgs:    []string{"--arg1", "--arg2", "value"},
			},
			wantContains: []string{
				"FROM node:",
				"npm install --save example-package",
				"COPY --from=builder --chown=appuser:appgroup /build/node_modules /app/node_modules",
				"echo \"exec npx $(echo example-package | sed 's/@[^@/]*$//'), \"--arg1\", \"--arg2\", \"value\"\" >> entrypoint.sh",
				"ENTRYPOINT [\"./entrypoint.sh\"]",
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
				MCPArgs:       []string{"--arg1", "--arg2", "value"},
				CACertContent: "-----BEGIN CERTIFICATE-----\nMIICertificateContent\n-----END CERTIFICATE-----",
			},
			wantContains: []string{
				"FROM node:",
				"npm install --save example-package",
				"echo \"exec npx $(echo example-package | sed 's/@[^@/]*$//'), \"--arg1\", \"--arg2\", \"value\"\" >> entrypoint.sh",
				"ENTRYPOINT [\"./entrypoint.sh\"]",
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
				MCPArgs:    []string{"--arg1", "--arg2", "value"},
			},
			wantContains: []string{
				"FROM golang:",
				"if ! echo \"$package\" | grep -q '@'; then",
				"package=\"${package}@latest\"",
				"go install \"$package\"",
				"FROM alpine:",
				"COPY --from=builder --chown=appuser:appgroup /app/mcp-server /app/mcp-server",
				"ENTRYPOINT [\"/app/mcp-server\", \"--arg1\", \"--arg2\", \"value\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`, // Match builder stage
				`FROM alpine:\d+\.\d+`,                   // Match runtime stage
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
				MCPArgs:       []string{"--arg1", "--arg2", "value"},
				CACertContent: "-----BEGIN CERTIFICATE-----\nMIICertificateContent\n-----END CERTIFICATE-----",
			},
			wantContains: []string{
				"FROM golang:",
				"if ! echo \"$package\" | grep -q '@'; then",
				"package=\"${package}@latest\"",
				"go install \"$package\"",
				"FROM alpine:",
				"ENTRYPOINT [\"/app/mcp-server\", \"--arg1\", \"--arg2\", \"value\"]",
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`, // Match builder stage
				`FROM alpine:\d+\.\d+`,                   // Match runtime stage
			},
			wantNotContains: []string{},
			wantErr:         false,
		},
		{
			name:          "GO transport with local path",
			transportType: TransportTypeGO,
			data: TemplateData{
				MCPPackage:  "./cmd/server",
				MCPArgs:     []string{"--arg1", "value"},
				IsLocalPath: true,
			},
			wantContains: []string{
				"FROM golang:",
				"COPY . /build/",
				"go build -o /app/mcp-server ./cmd/server",
				"FROM alpine:",
				"COPY --from=builder --chown=appuser:appgroup /app/mcp-server /app/mcp-server",
				"COPY --from=builder --chown=appuser:appgroup /build/ /app/",
				"ENTRYPOINT [\"/app/mcp-server\", \"--arg1\", \"value\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`, // Match builder stage
				`FROM alpine:\d+\.\d+`,                   // Match runtime stage
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
				MCPArgs:     []string{},
				IsLocalPath: true,
			},
			wantContains: []string{
				"FROM golang:",
				"COPY . /build/",
				"go build -o /app/mcp-server .",
				"FROM alpine:",
				"COPY --from=builder --chown=appuser:appgroup /app/mcp-server /app/mcp-server",
				"ENTRYPOINT [\"/app/mcp-server\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine AS builder`, // Match builder stage
				`FROM alpine:\d+\.\d+`,                   // Match runtime stage
			},
			wantNotContains: []string{
				"Add custom CA certificate",
			},
			wantErr: false,
		},
		{
			name:          "Unsupported transport",
			transportType: "unsupported",
			data: TemplateData{
				MCPPackage: "example-package",
				MCPArgs:    []string{"--arg1", "--arg2", "value"},
			},
			wantContains:    nil,
			wantNotContains: nil,
			wantErr:         true,
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

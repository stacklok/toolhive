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
				"apt-get install -y --no-install-recommends ca-certificates",
				"pip install --no-cache-dir uv",
				"ENTRYPOINT [\"uvx\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
			},
			wantMatches: []string{
				`FROM python:\d+\.\d+-slim`, // Match any Python version
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
				"apt-get install -y --no-install-recommends ca-certificates",
				"pip install --no-cache-dir uv",
				"ENTRYPOINT [\"uvx\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM python:\d+\.\d+-slim`, // Match any Python version
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
				"ENTRYPOINT [\"npx\", \"--yes\", \"--\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
			},
			wantMatches: []string{
				`FROM node:\d+-alpine`, // Match any Node version
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
				"ENTRYPOINT [\"npx\", \"--yes\", \"--\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM node:\d+-alpine`, // Match any Node version
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
				"ENTRYPOINT [\"go\", \"run\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine`, // Match any Go version
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
				"ENTRYPOINT [\"go\", \"run\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
				"Add custom CA certificate BEFORE any network operations",
				"COPY ca-cert.crt /tmp/custom-ca.crt",
				"cat /tmp/custom-ca.crt >> /etc/ssl/certs/ca-certificates.crt",
				"update-ca-certificates",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine`, // Match any Go version
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
				"COPY . /app/",
				"ENTRYPOINT [\"go\", \"run\", \"./cmd/server\", \"--arg1\", \"value\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine`, // Match any Go version
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
				"COPY . /app/",
				"ENTRYPOINT [\"go\", \"run\", \".\"]",
			},
			wantMatches: []string{
				`FROM golang:\d+\.\d+-alpine`, // Match any Go version
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

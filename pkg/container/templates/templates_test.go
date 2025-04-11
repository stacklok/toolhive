package templates

import (
	"strings"
	"testing"
)

func TestGetDockerfileTemplate(t *testing.T) {
	tests := []struct {
		name          string
		transportType TransportType
		data          TemplateData
		wantContains  []string
		wantErr       bool
	}{
		{
			name:          "UVX transport",
			transportType: TransportTypeUVX,
			data: TemplateData{
				MCPPackage: "example-package",
				MCPArgs:    []string{"--arg1", "--arg2", "value"},
			},
			wantContains: []string{
				"FROM python:3.12-slim",
				"RUN pip install uv",
				"ENTRYPOINT [\"uvx\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
			},
			wantErr: false,
		},
		{
			name:          "NPX transport",
			transportType: TransportTypeNPX,
			data: TemplateData{
				MCPPackage: "example-package",
				MCPArgs:    []string{"--arg1", "--arg2", "value"},
			},
			wantContains: []string{
				"FROM node:22-slim",
				"ENTRYPOINT [\"npx\", \"--yes\", \"--\", \"example-package\", \"--arg1\", \"--arg2\", \"value\"]",
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
			wantContains: nil,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetDockerfileTemplate(tt.transportType, tt.data)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetDockerfileTemplate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err != nil {
				return
			}

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("GetDockerfileTemplate() = %v, want to contain %v", got, want)
				}
			}
		})
	}
}

func TestParseTransportType(t *testing.T) {
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
			name:    "Unsupported transport",
			s:       "unsupported",
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

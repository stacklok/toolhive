// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestRegistryServer_Validate(t *testing.T) {
	t.Parallel()
	url := "http://example.com/mcp"
	pkg := "github.com/example/mcp-server"

	tests := []struct {
		name    string
		server  *RegistryServer
		wantErr bool
	}{
		{
			name: "Remote server with URL is valid",
			server: &RegistryServer{
				BaseMCPServer: BaseMCPServer{
					Remote: true,
				},
				URL: &url,
			},
			wantErr: false,
		},
		{
			name: "Container server with package is valid",
			server: &RegistryServer{
				BaseMCPServer: BaseMCPServer{
					Remote: false,
				},
				Package: &pkg,
			},
			wantErr: false,
		},
		{
			name: "Remote server without URL is invalid",
			server: &RegistryServer{
				BaseMCPServer: BaseMCPServer{
					Remote: true,
				},
			},
			wantErr: true,
		},
		{
			name: "Container server without package is invalid",
			server: &RegistryServer{
				BaseMCPServer: BaseMCPServer{
					Remote: false,
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.server.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("RegistryServer.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToolDetailsToJSON(t *testing.T) {
	t.Parallel()
	tool := mcp.Tool{
		Name:        "test_tool",
		Description: "A test tool",
	}

	json, err := ToolDetailsToJSON(tool)
	if err != nil {
		t.Fatalf("ToolDetailsToJSON() error = %v", err)
	}

	if json == "" {
		t.Error("ToolDetailsToJSON() returned empty string")
	}

	// Try to parse it back
	parsed, err := ToolDetailsFromJSON(json)
	if err != nil {
		t.Fatalf("ToolDetailsFromJSON() error = %v", err)
	}

	if parsed.Name != tool.Name {
		t.Errorf("Tool name mismatch: got %v, want %v", parsed.Name, tool.Name)
	}

	if parsed.Description != tool.Description {
		t.Errorf("Tool description mismatch: got %v, want %v", parsed.Description, tool.Description)
	}
}

func TestTokenMetrics_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		metrics *TokenMetrics
		wantErr bool
	}{
		{
			name: "Valid metrics with savings",
			metrics: &TokenMetrics{
				BaselineTokens:    1000,
				ReturnedTokens:    600,
				TokensSaved:       400,
				SavingsPercentage: 40.0,
			},
			wantErr: false,
		},
		{
			name: "Valid metrics with no savings",
			metrics: &TokenMetrics{
				BaselineTokens:    1000,
				ReturnedTokens:    1000,
				TokensSaved:       0,
				SavingsPercentage: 0.0,
			},
			wantErr: false,
		},
		{
			name: "Invalid: tokens saved doesn't match",
			metrics: &TokenMetrics{
				BaselineTokens:    1000,
				ReturnedTokens:    600,
				TokensSaved:       500, // Should be 400
				SavingsPercentage: 40.0,
			},
			wantErr: true,
		},
		{
			name: "Invalid: savings percentage doesn't match",
			metrics: &TokenMetrics{
				BaselineTokens:    1000,
				ReturnedTokens:    600,
				TokensSaved:       400,
				SavingsPercentage: 50.0, // Should be 40.0
			},
			wantErr: true,
		},
		{
			name: "Invalid: non-zero percentage with zero baseline",
			metrics: &TokenMetrics{
				BaselineTokens:    0,
				ReturnedTokens:    0,
				TokensSaved:       0,
				SavingsPercentage: 10.0, // Should be 0
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.metrics.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("TokenMetrics.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBackendWithRegistry_EffectiveDescription(t *testing.T) {
	t.Parallel()
	registryDesc := "Registry description"
	backendDesc := "Backend description"

	tests := []struct {
		name string
		w    *BackendWithRegistry
		want *string
	}{
		{
			name: "Uses registry description when available",
			w: &BackendWithRegistry{
				Backend: BackendServer{
					Description: &backendDesc,
				},
				Registry: &RegistryServer{
					BaseMCPServer: BaseMCPServer{
						Description: &registryDesc,
					},
				},
			},
			want: &registryDesc,
		},
		{
			name: "Uses backend description when no registry",
			w: &BackendWithRegistry{
				Backend: BackendServer{
					Description: &backendDesc,
				},
				Registry: nil,
			},
			want: &backendDesc,
		},
		{
			name: "Returns nil when no description",
			w: &BackendWithRegistry{
				Backend:  BackendServer{},
				Registry: nil,
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.w.EffectiveDescription()
			if (got == nil) != (tt.want == nil) {
				t.Errorf("BackendWithRegistry.EffectiveDescription() = %v, want %v", got, tt.want)
			}
			if got != nil && tt.want != nil && *got != *tt.want {
				t.Errorf("BackendWithRegistry.EffectiveDescription() = %v, want %v", *got, *tt.want)
			}
		})
	}
}

func TestBackendWithRegistry_ServerNameForTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		w    *BackendWithRegistry
		want string
	}{
		{
			name: "Uses registry name when available",
			w: &BackendWithRegistry{
				Backend: BackendServer{
					Name: "backend-name",
				},
				Registry: &RegistryServer{
					BaseMCPServer: BaseMCPServer{
						Name: "registry-name",
					},
				},
			},
			want: "registry-name",
		},
		{
			name: "Uses backend name when no registry",
			w: &BackendWithRegistry{
				Backend: BackendServer{
					Name: "backend-name",
				},
				Registry: nil,
			},
			want: "backend-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.w.ServerNameForTools(); got != tt.want {
				t.Errorf("BackendWithRegistry.ServerNameForTools() = %v, want %v", got, tt.want)
			}
		})
	}
}

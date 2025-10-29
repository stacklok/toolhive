package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestYAMLLoader_Load(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		yaml    string
		envVars map[string]string
		want    func(*testing.T, *Config)
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid minimal configuration",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.Name != "test-vmcp" {
					t.Errorf("Name = %v, want test-vmcp", cfg.Name)
				}
				if cfg.GroupRef != "test-group" {
					t.Errorf("GroupRef = %v, want test-group", cfg.GroupRef)
				}
				if cfg.IncomingAuth.Type != "anonymous" {
					t.Errorf("IncomingAuth.Type = %v, want anonymous", cfg.IncomingAuth.Type)
				}
				if cfg.OutgoingAuth.Source != "inline" {
					t.Errorf("OutgoingAuth.Source = %v, want inline", cfg.OutgoingAuth.Source)
				}
				if cfg.Aggregation.ConflictResolution != vmcp.ConflictStrategyPrefix {
					t.Errorf("ConflictResolution = %v, want prefix", cfg.Aggregation.ConflictResolution)
				}
			},
			wantErr: false,
		},
		{
			name: "valid OIDC configuration with env vars",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: oidc
  oidc:
    issuer: https://auth.example.com
    client_id: test-client
    client_secret_env: TEST_SECRET
    audience: vmcp
    scopes:
      - openid
      - profile

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			envVars: map[string]string{
				"TEST_SECRET": "my-secret-value",
			},
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.IncomingAuth.Type != "oidc" {
					t.Errorf("IncomingAuth.Type = %v, want oidc", cfg.IncomingAuth.Type)
				}
				if cfg.IncomingAuth.OIDC == nil {
					t.Fatal("IncomingAuth.OIDC is nil")
				}
				if cfg.IncomingAuth.OIDC.Issuer != "https://auth.example.com" {
					t.Errorf("OIDC.Issuer = %v, want https://auth.example.com", cfg.IncomingAuth.OIDC.Issuer)
				}
				if cfg.IncomingAuth.OIDC.ClientSecret != "my-secret-value" {
					t.Errorf("OIDC.ClientSecret = %v, want my-secret-value", cfg.IncomingAuth.OIDC.ClientSecret)
				}
			},
			wantErr: false,
		},
		{
			name: "valid configuration with token cache",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"

token_cache:
  provider: memory
  config:
    max_entries: 1000
    ttl_offset: 5m
`,
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.TokenCache == nil {
					t.Fatal("TokenCache is nil")
				}
				if cfg.TokenCache.Provider != CacheProviderMemory {
					t.Errorf("TokenCache.Provider = %v, want memory", cfg.TokenCache.Provider)
				}
				if cfg.TokenCache.Memory == nil {
					t.Fatal("TokenCache.Memory is nil")
				}
				if cfg.TokenCache.Memory.MaxEntries != 1000 {
					t.Errorf("Memory.MaxEntries = %v, want 1000", cfg.TokenCache.Memory.MaxEntries)
				}
				if cfg.TokenCache.Memory.TTLOffset != 5*time.Minute {
					t.Errorf("Memory.TTLOffset = %v, want 5m", cfg.TokenCache.Memory.TTLOffset)
				}
			},
			wantErr: false,
		},
		{
			name: "valid configuration with composite tools",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"

composite_tools:
  - name: deploy_workflow
    description: Deploy and notify
    parameters:
      pr_number:
        type: integer
    timeout: 30m
    steps:
      - id: merge
        type: tool
        tool: github.merge_pr
        arguments:
          pr: "{{.params.pr_number}}"
      - id: notify
        type: tool
        tool: slack.post_message
        arguments:
          message: "Deployed PR {{.params.pr_number}}"
        depends_on:
          - merge
`,
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				if len(cfg.CompositeTools) != 1 {
					t.Fatalf("CompositeTools length = %v, want 1", len(cfg.CompositeTools))
				}
				tool := cfg.CompositeTools[0]
				if tool.Name != "deploy_workflow" {
					t.Errorf("Tool.Name = %v, want deploy_workflow", tool.Name)
				}
				if tool.Timeout != 30*time.Minute {
					t.Errorf("Tool.Timeout = %v, want 30m", tool.Timeout)
				}
				if len(tool.Steps) != 2 {
					t.Errorf("Tool.Steps length = %v, want 2", len(tool.Steps))
				}
			},
			wantErr: false,
		},
		{
			name: "invalid YAML syntax",
			yaml: `
name: test-vmcp
group: test-group
incoming_auth
  type: anonymous
`,
			wantErr: true,
			errMsg:  "failed to parse YAML",
		},
		{
			name: "missing environment variable",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: oidc
  oidc:
    issuer: https://auth.example.com
    client_id: test-client
    client_secret_env: MISSING_VAR
    audience: vmcp

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			wantErr: true,
			errMsg:  "environment variable MISSING_VAR not set",
		},
		{
			name: "invalid duration format",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"

token_cache:
  provider: memory
  config:
    max_entries: 1000
    ttl_offset: invalid-duration
`,
			wantErr: true,
			errMsg:  "invalid ttl_offset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Set up environment variables
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			// Create temporary file with YAML content
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(tmpFile, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}

			// Load configuration
			loader := NewYAMLLoader(tmpFile)
			cfg, err := loader.Load()

			// Check error expectation
			if (err != nil) != tt.wantErr {
				t.Errorf("Load() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Load() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
				return
			}

			// Verify configuration
			if tt.want != nil && cfg != nil {
				tt.want(t, cfg)
			}
		})
	}
}

func TestYAMLLoader_LoadFileNotFound(t *testing.T) {
	t.Parallel()
	loader := NewYAMLLoader("/non/existent/file.yaml")
	_, err := loader.Load()

	if err == nil {
		t.Error("Load() expected error for non-existent file, got nil")
	}

	if !strings.Contains(err.Error(), "failed to read config file") {
		t.Errorf("Load() error = %v, want to contain 'failed to read config file'", err)
	}
}

func TestYAMLLoader_IntegrationWithValidator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		yaml       string
		envVars    map[string]string
		shouldPass bool
		errMsg     string
	}{
		{
			name: "valid configuration passes validation",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			shouldPass: true,
		},
		{
			name: "configuration with missing name fails validation",
			yaml: `
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			shouldPass: false,
			errMsg:     "name is required",
		},
		{
			name: "configuration with invalid auth type fails validation",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: invalid_type

outgoing_auth:
  source: inline
  default:
    type: pass_through

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			shouldPass: false,
			errMsg:     "incoming_auth.type must be one of",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Set up environment variables
			for k, v := range tt.envVars {
				os.Setenv(k, v)
				defer os.Unsetenv(k)
			}

			// Create temporary file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(tmpFile, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}

			// Load and validate
			loader := NewYAMLLoader(tmpFile)
			cfg, err := loader.Load()
			if err != nil {
				if tt.shouldPass {
					t.Fatalf("Load() unexpected error = %v", err)
				}
				return
			}

			validator := NewValidator()
			err = validator.Validate(cfg)

			if tt.shouldPass && err != nil {
				t.Errorf("Validate() unexpected error = %v", err)
			}

			if !tt.shouldPass && err == nil {
				t.Error("Validate() expected error, got nil")
			}

			if !tt.shouldPass && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

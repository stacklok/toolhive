package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/env/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// createMockEnvReader creates a mock env.Reader with expectations based on the envVars map.
func createMockEnvReader(t *testing.T, envVars map[string]string) *mocks.MockReader {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockEnv := mocks.NewMockReader(ctrl)

	// Set up expectations for each env var
	for key, value := range envVars {
		mockEnv.EXPECT().Getenv(key).Return(value).AnyTimes()
	}

	// For any other keys, return empty string
	mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

	return mockEnv
}

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
    type: unauthenticated

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
				if cfg.Group != "test-group" {
					t.Errorf("Group = %v, want test-group", cfg.Group)
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
    type: unauthenticated

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
				if cfg.IncomingAuth.OIDC.ClientSecretEnv != "TEST_SECRET" {
					t.Errorf("OIDC.ClientSecretEnv = %v, want TEST_SECRET", cfg.IncomingAuth.OIDC.ClientSecretEnv)
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
    type: unauthenticated

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
				if cfg.TokenCache.Memory.TTLOffset != Duration(5*time.Minute) {
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
    type: unauthenticated

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
				if tool.Timeout != Duration(30*time.Minute) {
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
			name: "OIDC with unset environment variable is allowed (validation happens at runtime)",
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
    type: unauthenticated

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.IncomingAuth.OIDC == nil {
					t.Fatal("IncomingAuth.OIDC is nil")
				}
				// Verify the env var name is stored (not resolved)
				if cfg.IncomingAuth.OIDC.ClientSecretEnv != "MISSING_VAR" {
					t.Errorf("OIDC.ClientSecretEnv = %v, want MISSING_VAR", cfg.IncomingAuth.OIDC.ClientSecretEnv)
				}
			},
			wantErr: false,
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
    type: unauthenticated

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
		{
			name: "composite tool with missing parameter type",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  default:
    type: unauthenticated

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"

composite_tools:
  - name: test_tool
    description: Test tool
    timeout: 5m
    parameters:
      param1:
        default: "value"
    steps:
      - id: step1
        type: tool
        tool: some.tool
`,
			wantErr: true,
			errMsg:  "missing 'type' field",
		},
		{
			name: "header_injection with header_value_env resolves environment variable",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  backends:
    github:
      type: header_injection
      header_injection:
        header_name: "Authorization"
        header_value_env: "GITHUB_TOKEN"

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			envVars: map[string]string{
				"GITHUB_TOKEN": "secret-token-123",
			},
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				backend, ok := cfg.OutgoingAuth.Backends["github"]
				if !ok {
					t.Fatal("github backend not found")
				}
				if backend.Type != "header_injection" {
					t.Errorf("Backend.Type = %v, want header_injection", backend.Type)
				}
				// Verify the resolved value is in metadata
				headerValue, ok := backend.Metadata["header_value"].(string)
				if !ok {
					t.Fatal("header_value not found in metadata")
				}
				if headerValue != "secret-token-123" {
					t.Errorf("header_value = %v, want secret-token-123", headerValue)
				}
			},
			wantErr: false,
		},
		{
			name: "header_injection with literal header_value works",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  backends:
    api-service:
      type: header_injection
      header_injection:
        header_name: "X-API-Version"
        header_value: "v1"

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			want: func(t *testing.T, cfg *Config) {
				t.Helper()
				backend, ok := cfg.OutgoingAuth.Backends["api-service"]
				if !ok {
					t.Fatal("api-service backend not found")
				}
				headerValue, ok := backend.Metadata["header_value"].(string)
				if !ok {
					t.Fatal("header_value not found in metadata")
				}
				if headerValue != "v1" {
					t.Errorf("header_value = %v, want v1", headerValue)
				}
			},
			wantErr: false,
		},
		{
			name: "header_injection fails when env var not set",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  backends:
    github:
      type: header_injection
      header_injection:
        header_name: "Authorization"
        header_value_env: "MISSING_TOKEN"

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			wantErr: true,
			errMsg:  "environment variable MISSING_TOKEN not set",
		},
		{
			name: "header_injection fails when both header_value and header_value_env set",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  backends:
    github:
      type: header_injection
      header_injection:
        header_name: "Authorization"
        header_value: "literal-value"
        header_value_env: "ENV_VALUE"

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			wantErr: true,
			errMsg:  "only one of header_value or header_value_env must be set",
		},
		{
			name: "header_injection fails when neither header_value nor header_value_env set",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  backends:
    github:
      type: header_injection
      header_injection:
        header_name: "Authorization"

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			wantErr: true,
			errMsg:  "either header_value or header_value_env must be set",
		},
		{
			name: "header_injection fails when env var is empty string",
			yaml: `
name: test-vmcp
group: test-group

incoming_auth:
  type: anonymous

outgoing_auth:
  source: inline
  backends:
    github:
      type: header_injection
      header_injection:
        header_name: "Authorization"
        header_value_env: "EMPTY_TOKEN"

aggregation:
  conflict_resolution: prefix
  conflict_resolution_config:
    prefix_format: "{workload}_"
`,
			envVars: map[string]string{
				"EMPTY_TOKEN": "",
			},
			wantErr: true,
			errMsg:  "environment variable EMPTY_TOKEN not set or empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create mock env reader with test-specific env vars
			mockEnv := createMockEnvReader(t, tt.envVars)

			// Create temporary file with YAML content
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(tmpFile, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}

			// Load configuration
			loader := NewYAMLLoader(tmpFile, mockEnv)
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
	envReader := &env.OSReader{}
	loader := NewYAMLLoader("/non/existent/file.yaml", envReader)
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
    type: unauthenticated

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
    type: unauthenticated

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
    type: unauthenticated

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

			// Create mock env reader with test-specific env vars
			mockEnv := createMockEnvReader(t, tt.envVars)

			// Create temporary file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(tmpFile, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("Failed to write temp file: %v", err)
			}

			// Load and validate
			loader := NewYAMLLoader(tmpFile, mockEnv)
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

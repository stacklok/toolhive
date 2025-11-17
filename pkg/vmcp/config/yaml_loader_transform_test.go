package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/env/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
)

// TestYAMLLoader_transformBackendAuthStrategy tests the critical auth strategy transformation logic
// including environment variable resolution, mutual exclusivity validation, and strategy-specific config.
func TestYAMLLoader_transformBackendAuthStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     *rawBackendAuthStrategy
		envVars map[string]string
		verify  func(t *testing.T, strategy *BackendAuthStrategy)
		wantErr bool
		errMsg  string
	}{
		{
			name: "header_injection with literal value",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer token123",
				},
			},
			verify: func(t *testing.T, strategy *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "Bearer token123", strategy.Metadata[strategies.MetadataHeaderValue])
			},
		},
		{
			name: "header_injection resolves env var correctly",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
					HeaderName:     "X-API-Key",
					HeaderValueEnv: "API_KEY",
				},
			},
			envVars: map[string]string{
				"API_KEY": "secret-key-value",
			},
			verify: func(t *testing.T, strategy *BackendAuthStrategy) {
				t.Helper()
				assert.Equal(t, "secret-key-value", strategy.Metadata[strategies.MetadataHeaderValue])
			},
		},
		{
			name: "header_injection fails when both value and env set (mutual exclusivity)",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
					HeaderName:     "Authorization",
					HeaderValue:    "literal",
					HeaderValueEnv: "ENV_VAR",
				},
			},
			wantErr: true,
			errMsg:  "only one of header_value or header_value_env must be set",
		},
		{
			name: "header_injection fails when neither value nor env set",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
					HeaderName: "Authorization",
				},
			},
			wantErr: true,
			errMsg:  "either header_value or header_value_env must be set",
		},
		{
			name: "header_injection fails when env var not set",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
					HeaderName:     "Authorization",
					HeaderValueEnv: "MISSING_VAR",
				},
			},
			wantErr: true,
			errMsg:  "environment variable MISSING_VAR not set or empty",
		},
		{
			name: "header_injection fails when env var is empty string",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
					HeaderName:     "Authorization",
					HeaderValueEnv: "EMPTY_VAR",
				},
			},
			envVars: map[string]string{
				"EMPTY_VAR": "",
			},
			wantErr: true,
			errMsg:  "environment variable EMPTY_VAR not set or empty",
		},
		{
			name: "header_injection fails when config block missing",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeHeaderInjection,
			},
			wantErr: true,
			errMsg:  "header_injection configuration is required",
		},
		{
			name: "token_exchange validates env var is set",
			raw: &rawBackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &rawTokenExchangeAuth{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "client-123",
					ClientSecretEnv: "CLIENT_SECRET",
				},
			},
			envVars: map[string]string{
				"CLIENT_SECRET": "secret-value",
			},
			verify: func(t *testing.T, strategy *BackendAuthStrategy) {
				t.Helper()
				// Verify env var name is stored (not resolved) for lazy evaluation
				assert.Equal(t, "CLIENT_SECRET", strategy.Metadata["client_secret_env"])
			},
		},
		{
			name: "token_exchange fails when env var not set",
			raw: &rawBackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &rawTokenExchangeAuth{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "client-123",
					ClientSecretEnv: "MISSING_SECRET",
				},
			},
			wantErr: true,
			errMsg:  "environment variable MISSING_SECRET not set",
		},
		{
			name: "token_exchange fails when config block missing",
			raw: &rawBackendAuthStrategy{
				Type: "token_exchange",
			},
			wantErr: true,
			errMsg:  "token_exchange configuration is required",
		},
		{
			name: "unauthenticated strategy requires no extra config",
			raw: &rawBackendAuthStrategy{
				Type: strategies.StrategyTypeUnauthenticated,
			},
			verify: func(t *testing.T, strategy *BackendAuthStrategy) {
				t.Helper()
				assert.Empty(t, strategy.Metadata)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockEnv := mocks.NewMockReader(ctrl)
			for key, value := range tt.envVars {
				mockEnv.EXPECT().Getenv(key).Return(value).AnyTimes()
			}
			mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

			loader := &YAMLLoader{envReader: mockEnv}
			strategy, err := loader.transformBackendAuthStrategy(tt.raw)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, strategy)
				if tt.verify != nil {
					tt.verify(t, strategy)
				}
			}
		})
	}
}

// TestYAMLLoader_transformTokenCache tests duration parsing which can fail.
func TestYAMLLoader_transformTokenCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        *rawTokenCache
		wantErr    bool
		errMsg     string
		wantOffset time.Duration
	}{
		{
			name: "memory cache parses duration correctly",
			raw: &rawTokenCache{
				Provider: CacheProviderMemory,
				Config: struct {
					MaxEntries int    `yaml:"max_entries"`
					TTLOffset  string `yaml:"ttl_offset"`
					Address    string `yaml:"address"`
					DB         int    `yaml:"db"`
					KeyPrefix  string `yaml:"key_prefix"`
					Password   string `yaml:"password"`
				}{
					MaxEntries: 1000,
					TTLOffset:  "5m",
				},
			},
			wantOffset: 5 * time.Minute,
		},
		{
			name: "redis cache parses duration correctly",
			raw: &rawTokenCache{
				Provider: CacheProviderRedis,
				Config: struct {
					MaxEntries int    `yaml:"max_entries"`
					TTLOffset  string `yaml:"ttl_offset"`
					Address    string `yaml:"address"`
					DB         int    `yaml:"db"`
					KeyPrefix  string `yaml:"key_prefix"`
					Password   string `yaml:"password"`
				}{
					TTLOffset: "10m",
				},
			},
			wantOffset: 10 * time.Minute,
		},
		{
			name: "invalid duration returns error",
			raw: &rawTokenCache{
				Provider: CacheProviderMemory,
				Config: struct {
					MaxEntries int    `yaml:"max_entries"`
					TTLOffset  string `yaml:"ttl_offset"`
					Address    string `yaml:"address"`
					DB         int    `yaml:"db"`
					KeyPrefix  string `yaml:"key_prefix"`
					Password   string `yaml:"password"`
				}{
					TTLOffset: "invalid",
				},
			},
			wantErr: true,
			errMsg:  "invalid ttl_offset",
		},
		{
			name: "complex duration like 1h30m parses correctly",
			raw: &rawTokenCache{
				Provider: CacheProviderMemory,
				Config: struct {
					MaxEntries int    `yaml:"max_entries"`
					TTLOffset  string `yaml:"ttl_offset"`
					Address    string `yaml:"address"`
					DB         int    `yaml:"db"`
					KeyPrefix  string `yaml:"key_prefix"`
					Password   string `yaml:"password"`
				}{
					TTLOffset: "1h30m",
				},
			},
			wantOffset: 90 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			cfg, err := loader.transformTokenCache(tt.raw)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
				// Verify duration was parsed correctly
				if cfg.Memory != nil {
					assert.Equal(t, Duration(tt.wantOffset), cfg.Memory.TTLOffset)
				} else if cfg.Redis != nil {
					assert.Equal(t, Duration(tt.wantOffset), cfg.Redis.TTLOffset)
				}
			}
		})
	}
}

// TestYAMLLoader_transformOperational tests duration parsing in multiple fields.
func TestYAMLLoader_transformOperational(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     *rawOperational
		wantErr bool
		errMsg  string
	}{
		{
			name: "parses all duration fields correctly",
			raw: &rawOperational{
				Timeouts: struct {
					Default     string            `yaml:"default"`
					PerWorkload map[string]string `yaml:"per_workload"`
				}{
					Default: "30s",
					PerWorkload: map[string]string{
						"slow-backend": "1m",
						"fast-backend": "10s",
					},
				},
				FailureHandling: struct {
					HealthCheckInterval string `yaml:"health_check_interval"`
					UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
					PartialFailureMode  string `yaml:"partial_failure_mode"`
					CircuitBreaker      struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					} `yaml:"circuit_breaker"`
				}{
					HealthCheckInterval: "5s",
					CircuitBreaker: struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					}{
						Enabled: true,
						Timeout: "30s",
					},
				},
			},
		},
		{
			name: "invalid default timeout returns error",
			raw: &rawOperational{
				Timeouts: struct {
					Default     string            `yaml:"default"`
					PerWorkload map[string]string `yaml:"per_workload"`
				}{
					Default: "invalid",
				},
				FailureHandling: struct {
					HealthCheckInterval string `yaml:"health_check_interval"`
					UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
					PartialFailureMode  string `yaml:"partial_failure_mode"`
					CircuitBreaker      struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					} `yaml:"circuit_breaker"`
				}{
					HealthCheckInterval: "10s",
				},
			},
			wantErr: true,
			errMsg:  "invalid default timeout",
		},
		{
			name: "invalid per-workload timeout returns error with workload name",
			raw: &rawOperational{
				Timeouts: struct {
					Default     string            `yaml:"default"`
					PerWorkload map[string]string `yaml:"per_workload"`
				}{
					Default: "30s",
					PerWorkload: map[string]string{
						"bad-backend": "invalid-duration",
					},
				},
				FailureHandling: struct {
					HealthCheckInterval string `yaml:"health_check_interval"`
					UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
					PartialFailureMode  string `yaml:"partial_failure_mode"`
					CircuitBreaker      struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					} `yaml:"circuit_breaker"`
				}{
					HealthCheckInterval: "10s",
				},
			},
			wantErr: true,
			errMsg:  "invalid timeout for workload bad-backend",
		},
		{
			name: "invalid health check interval returns error",
			raw: &rawOperational{
				FailureHandling: struct {
					HealthCheckInterval string `yaml:"health_check_interval"`
					UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
					PartialFailureMode  string `yaml:"partial_failure_mode"`
					CircuitBreaker      struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					} `yaml:"circuit_breaker"`
				}{
					HealthCheckInterval: "not-a-duration",
				},
			},
			wantErr: true,
			errMsg:  "invalid health_check_interval",
		},
		{
			name: "invalid circuit breaker timeout returns error",
			raw: &rawOperational{
				FailureHandling: struct {
					HealthCheckInterval string `yaml:"health_check_interval"`
					UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
					PartialFailureMode  string `yaml:"partial_failure_mode"`
					CircuitBreaker      struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					} `yaml:"circuit_breaker"`
				}{
					HealthCheckInterval: "10s",
					CircuitBreaker: struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					}{
						Enabled: true,
						Timeout: "bad-duration",
					},
				},
			},
			wantErr: true,
			errMsg:  "invalid circuit_breaker timeout",
		},
		{
			name: "circuit breaker disabled skips timeout parsing",
			raw: &rawOperational{
				FailureHandling: struct {
					HealthCheckInterval string `yaml:"health_check_interval"`
					UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
					PartialFailureMode  string `yaml:"partial_failure_mode"`
					CircuitBreaker      struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					} `yaml:"circuit_breaker"`
				}{
					HealthCheckInterval: "10s",
					CircuitBreaker: struct {
						Enabled          bool   `yaml:"enabled"`
						FailureThreshold int    `yaml:"failure_threshold"`
						Timeout          string `yaml:"timeout"`
					}{
						Enabled: false,
						// Even with bad timeout, should not error since disabled
						Timeout: "invalid",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			cfg, err := loader.transformOperational(tt.raw)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, cfg)
			}
		})
	}
}

// TestYAMLLoader_transformCompositeTools tests parameter validation and duration parsing.
func TestYAMLLoader_transformCompositeTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     []*rawCompositeTool
		verify  func(t *testing.T, tools []*CompositeToolConfig)
		wantErr bool
		errMsg  string
	}{
		{
			name: "empty timeout defaults to zero",
			raw: []*rawCompositeTool{
				{
					Name:  "test",
					Steps: []*rawWorkflowStep{{ID: "step1", Tool: "test.tool"}},
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				assert.Equal(t, Duration(0), tools[0].Timeout)
			},
		},
		{
			name: "timeout parses correctly",
			raw: []*rawCompositeTool{
				{
					Name:    "test",
					Timeout: "5m",
					Steps:   []*rawWorkflowStep{{ID: "step1", Tool: "test.tool"}},
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				assert.Equal(t, Duration(5*time.Minute), tools[0].Timeout)
			},
		},
		{
			name: "invalid timeout returns error",
			raw: []*rawCompositeTool{
				{
					Name:    "bad",
					Timeout: "invalid",
					Steps:   []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			wantErr: true,
			errMsg:  "invalid timeout",
		},
		{
			name: "parameter missing type field returns error",
			raw: []*rawCompositeTool{
				{
					Name: "bad",
					Parameters: map[string]map[string]any{
						"param1": {
							"default": "value",
						},
					},
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			wantErr: true,
			errMsg:  "missing 'type' field",
		},
		{
			name: "parameter type not string returns error",
			raw: []*rawCompositeTool{
				{
					Name: "bad",
					Parameters: map[string]map[string]any{
						"param1": {
							"type": 123,
						},
					},
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			wantErr: true,
			errMsg:  "'type' field must be a string",
		},
		{
			name: "parameter with default value works",
			raw: []*rawCompositeTool{
				{
					Name: "test",
					Parameters: map[string]map[string]any{
						"version": {
							"type":    "string",
							"default": "latest",
						},
					},
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				assert.Equal(t, "string", tools[0].Parameters["version"].Type)
				assert.Equal(t, "latest", tools[0].Parameters["version"].Default)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			tools, err := loader.transformCompositeTools(tt.raw)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, tools)
				if tt.verify != nil {
					tt.verify(t, tools)
				}
			}
		})
	}
}

// TestYAMLLoader_transformWorkflowStep tests type inference, default timeouts, and duration parsing.
func TestYAMLLoader_transformWorkflowStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     *rawWorkflowStep
		verify  func(t *testing.T, step *WorkflowStepConfig)
		wantErr bool
		errMsg  string
	}{
		{
			name: "type inference: empty type with tool field infers 'tool'",
			raw: &rawWorkflowStep{
				ID:   "step1",
				Tool: "some.tool",
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				assert.Equal(t, "tool", step.Type)
			},
		},
		{
			name: "type inference: explicit type not overridden",
			raw: &rawWorkflowStep{
				ID:   "step1",
				Type: "elicitation",
				Tool: "some.tool",
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				assert.Equal(t, "elicitation", step.Type)
			},
		},
		{
			name: "elicitation without timeout gets 5 minute default",
			raw: &rawWorkflowStep{
				ID:      "ask",
				Type:    "elicitation",
				Message: "Approve?",
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				assert.Equal(t, Duration(5*time.Minute), step.Timeout)
			},
		},
		{
			name: "elicitation with explicit timeout overrides default",
			raw: &rawWorkflowStep{
				ID:      "ask",
				Type:    "elicitation",
				Message: "Approve?",
				Timeout: "10m",
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				assert.Equal(t, Duration(10*time.Minute), step.Timeout)
			},
		},
		{
			name: "tool step with timeout parses correctly",
			raw: &rawWorkflowStep{
				ID:      "slow",
				Type:    "tool",
				Tool:    "tool",
				Timeout: "2m",
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				assert.Equal(t, Duration(2*time.Minute), step.Timeout)
			},
		},
		{
			name: "invalid timeout returns error",
			raw: &rawWorkflowStep{
				ID:      "bad",
				Tool:    "tool",
				Timeout: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid timeout",
		},
		{
			name: "invalid retry delay returns error",
			raw: &rawWorkflowStep{
				ID:   "bad",
				Tool: "tool",
				OnError: &rawStepErrorHandling{
					Action:     "retry",
					RetryDelay: "not-a-duration",
				},
			},
			wantErr: true,
			errMsg:  "invalid retry_delay",
		},
		{
			name: "retry delay parses correctly",
			raw: &rawWorkflowStep{
				ID:   "step1",
				Tool: "tool",
				OnError: &rawStepErrorHandling{
					Action:     "retry",
					RetryCount: 3,
					RetryDelay: "5s",
				},
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				require.NotNil(t, step.OnError)
				assert.Equal(t, Duration(5*time.Second), step.OnError.RetryDelay)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			step, err := loader.transformWorkflowStep(tt.raw)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, step)
				if tt.verify != nil {
					tt.verify(t, step)
				}
			}
		})
	}
}

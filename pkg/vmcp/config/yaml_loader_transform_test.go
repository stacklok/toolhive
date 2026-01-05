package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/env/mocks"
	"github.com/stacklok/toolhive/pkg/telemetry"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestYAMLLoader_processBackendAuthStrategy tests the critical auth strategy processing logic
// including environment variable resolution, mutual exclusivity validation, and strategy-specific config.
func TestYAMLLoader_processBackendAuthStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		strategy *authtypes.BackendAuthStrategy
		envVars  map[string]string
		verify   func(t *testing.T, strategy *authtypes.BackendAuthStrategy)
		wantErr  bool
		errMsg   string
	}{
		{
			name: "header_injection with literal value",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:  "Authorization",
					HeaderValue: "Bearer token123",
				},
			},
			verify: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				require.NotNil(t, strategy.HeaderInjection)
				assert.Equal(t, "Bearer token123", strategy.HeaderInjection.HeaderValue)
			},
		},
		{
			name: "header_injection resolves env var correctly",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:     "X-API-Key",
					HeaderValueEnv: "API_KEY",
				},
			},
			envVars: map[string]string{
				"API_KEY": "secret-key-value",
			},
			verify: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				require.NotNil(t, strategy.HeaderInjection)
				assert.Equal(t, "secret-key-value", strategy.HeaderInjection.HeaderValue)
			},
		},
		{
			name: "header_injection fails when both value and env set (mutual exclusivity)",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:     "Authorization",
					HeaderValue:    "literal",
					HeaderValueEnv: "ENV_VAR",
				},
			},
			wantErr: true,
			errMsg:  "only one of headerValue or headerValueEnv must be set",
		},
		{
			name: "header_injection fails when neither value nor env set",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName: "Authorization",
				},
			},
			wantErr: true,
			errMsg:  "either headerValue or headerValueEnv must be set",
		},
		{
			name: "header_injection fails when env var not set",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
					HeaderName:     "Authorization",
					HeaderValueEnv: "MISSING_VAR",
				},
			},
			wantErr: true,
			errMsg:  "environment variable MISSING_VAR not set or empty",
		},
		{
			name: "header_injection fails when env var is empty string",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &authtypes.HeaderInjectionConfig{
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
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
			},
			wantErr: true,
			errMsg:  "headerInjection configuration is required",
		},
		{
			name: "tokenExchange validates env var is set",
			strategy: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "client-123",
					ClientSecretEnv: "CLIENT_SECRET",
				},
			},
			envVars: map[string]string{
				"CLIENT_SECRET": "secret-value",
			},
			verify: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				// Verify env var name is stored (not resolved) for lazy evaluation
				require.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "CLIENT_SECRET", strategy.TokenExchange.ClientSecretEnv)
			},
		},
		{
			name: "tokenExchange fails when env var not set",
			strategy: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
				TokenExchange: &authtypes.TokenExchangeConfig{
					TokenURL:        "https://auth.example.com/token",
					ClientID:        "client-123",
					ClientSecretEnv: "MISSING_SECRET",
				},
			},
			wantErr: true,
			errMsg:  "environment variable MISSING_SECRET not set",
		},
		{
			name: "tokenExchange fails when config block missing",
			strategy: &authtypes.BackendAuthStrategy{
				Type: "token_exchange",
			},
			wantErr: true,
			errMsg:  "tokenExchange configuration is required",
		},
		{
			name: "unauthenticated strategy requires no extra config",
			strategy: &authtypes.BackendAuthStrategy{
				Type: authtypes.StrategyTypeUnauthenticated,
			},
			verify: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				// Unauthenticated strategy has no additional config
				assert.Nil(t, strategy.HeaderInjection)
				assert.Nil(t, strategy.TokenExchange)
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
			err := loader.processBackendAuthStrategy("test", tt.strategy)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, tt.strategy)
				if tt.verify != nil {
					tt.verify(t, tt.strategy)
				}
			}
		})
	}
}

// TestYAMLLoader_processCompositeTool tests parameter validation.
func TestYAMLLoader_processCompositeTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tool    *CompositeToolConfig
		verify  func(t *testing.T, tool *CompositeToolConfig)
		wantErr bool
		errMsg  string
	}{
		{
			name: "parameter missing type field returns error",
			tool: &CompositeToolConfig{
				Name: "bad",
				Parameters: map[string]any{
					"properties": map[string]any{
						"param1": map[string]any{
							"type": "string",
						},
					},
					// Missing "type" at root level
				},
				Steps: []*WorkflowStepConfig{{ID: "s1"}},
			},
			wantErr: true,
			errMsg:  "parameters must have 'type' field",
		},
		{
			name: "parameter type not string returns error",
			tool: &CompositeToolConfig{
				Name: "bad",
				Parameters: map[string]any{
					"type": 123, // type must be string
					"properties": map[string]any{
						"param1": map[string]any{
							"type": "string",
						},
					},
				},
				Steps: []*WorkflowStepConfig{{ID: "s1"}},
			},
			wantErr: true,
			errMsg:  "'type' field must be a string",
		},
		{
			name: "parameter type must be object returns error",
			tool: &CompositeToolConfig{
				Name: "bad",
				Parameters: map[string]any{
					"type": "string", // must be "object" for parameter schemas
				},
				Steps: []*WorkflowStepConfig{{ID: "s1"}},
			},
			wantErr: true,
			errMsg:  "'type' must be 'object'",
		},
		{
			name: "parameter with default value works",
			tool: &CompositeToolConfig{
				Name: "test",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"version": map[string]any{
							"type":    "string",
							"default": "latest",
						},
					},
				},
				Steps: []*WorkflowStepConfig{{ID: "s1"}},
			},
			verify: func(t *testing.T, tool *CompositeToolConfig) {
				t.Helper()
				// Parameters is now map[string]any with JSON Schema format
				params := tool.Parameters
				assert.Equal(t, "object", params["type"])
				properties, ok := params["properties"].(map[string]any)
				require.True(t, ok, "properties should be a map")
				version, ok := properties["version"].(map[string]any)
				require.True(t, ok, "version property should be a map")
				assert.Equal(t, "string", version["type"])
				assert.Equal(t, "latest", version["default"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			err := loader.processCompositeTool(tt.tool)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, tt.tool)
				if tt.verify != nil {
					tt.verify(t, tt.tool)
				}
			}
		})
	}
}

// TestYAMLLoader_processWorkflowStep tests type inference and default timeouts.
func TestYAMLLoader_processWorkflowStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		step   *WorkflowStepConfig
		verify func(t *testing.T, step *WorkflowStepConfig)
	}{
		{
			name: "type inference: empty type with tool field infers 'tool'",
			step: &WorkflowStepConfig{
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
			step: &WorkflowStepConfig{
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
			step: &WorkflowStepConfig{
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
			name: "elicitation with explicit timeout keeps it",
			step: &WorkflowStepConfig{
				ID:      "ask",
				Type:    "elicitation",
				Message: "Approve?",
				Timeout: Duration(10 * time.Minute),
			},
			verify: func(t *testing.T, step *WorkflowStepConfig) {
				t.Helper()
				assert.Equal(t, Duration(10*time.Minute), step.Timeout)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			loader.processWorkflowStep(tt.step)

			require.NotNil(t, tt.step)
			if tt.verify != nil {
				tt.verify(t, tt.step)
			}
		})
	}
}

// TestYAMLLoader_Load_TelemetryConfig tests that telemetry configuration is preserved
// when loading from YAML.
func TestYAMLLoader_Load_TelemetryConfig(t *testing.T) {
	t.Parallel()

	yamlContent := `
name: telemetry-test
telemetry:
  endpoint: "localhost:4318"
  servicename: "test-service"
  serviceversion: "1.2.3"
  tracingenabled: true
  metricsenabled: true
  samplingrate: 0.75
  insecure: true
  enableprometheusmetricspath: true
  headers:
    Authorization: "Bearer token123"
    X-Custom-Header: "custom-value"
  environmentvariables:
    - "NODE_ENV"
    - "DEPLOYMENT_ENV"
`

	// Write temp file
	tmpFile, err := os.CreateTemp("", "telemetry-test-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(yamlContent)
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	// Load config
	ctrl := gomock.NewController(t)
	mockEnv := mocks.NewMockReader(ctrl)
	mockEnv.EXPECT().Getenv(gomock.Any()).Return("").AnyTimes()

	loader := NewYAMLLoader(tmpFile.Name(), mockEnv)
	cfg, err := loader.Load()
	require.NoError(t, err)

	// Verify telemetry config is fully preserved
	require.NotNil(t, cfg.Telemetry, "Telemetry config should not be nil")

	require.Equal(t, telemetry.Config{
		Endpoint:                    "localhost:4318",
		ServiceName:                 "test-service",
		ServiceVersion:              "1.2.3",
		TracingEnabled:              true,
		MetricsEnabled:              true,
		SamplingRate:                0.75,
		Insecure:                    true,
		EnablePrometheusMetricsPath: true,
		Headers:                     map[string]string{"Authorization": "Bearer token123", "X-Custom-Header": "custom-value"},
		EnvironmentVariables:        []string{"NODE_ENV", "DEPLOYMENT_ENV"},
		CustomAttributes:            nil,
	}, *cfg.Telemetry)
}

// TestYAMLLoader_StrictMode tests that unknown fields are rejected.
func TestYAMLLoader_StrictMode(t *testing.T) {
	t.Parallel()

	yamlContent := `
name: test
unknown_field: this should cause an error
`

	// Write temp file
	tmpFile, err := os.CreateTemp("", "strict-test-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(yamlContent)
	require.NoError(t, err)
	require.NoError(t, tmpFile.Close())

	// Load config
	ctrl := gomock.NewController(t)
	mockEnv := mocks.NewMockReader(ctrl)

	loader := NewYAMLLoader(tmpFile.Name(), mockEnv)
	_, err = loader.Load()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown_field")
}

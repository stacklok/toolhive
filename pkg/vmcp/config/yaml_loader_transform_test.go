package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/env/mocks"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestYAMLLoader_transformBackendAuthStrategy tests the critical auth strategy transformation logic
// including environment variable resolution, mutual exclusivity validation, and strategy-specific config.
func TestYAMLLoader_transformBackendAuthStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     *rawBackendAuthStrategy
		envVars map[string]string
		verify  func(t *testing.T, strategy *authtypes.BackendAuthStrategy)
		wantErr bool
		errMsg  string
	}{
		{
			name: "header_injection with literal value",
			raw: &rawBackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
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
			raw: &rawBackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
				HeaderInjection: &rawHeaderInjectionAuth{
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
			raw: &rawBackendAuthStrategy{
				Type: authtypes.StrategyTypeHeaderInjection,
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
				Type: authtypes.StrategyTypeHeaderInjection,
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
				Type: authtypes.StrategyTypeHeaderInjection,
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
				Type: authtypes.StrategyTypeHeaderInjection,
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
				Type: authtypes.StrategyTypeHeaderInjection,
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
			verify: func(t *testing.T, strategy *authtypes.BackendAuthStrategy) {
				t.Helper()
				// Verify env var name is stored (not resolved) for lazy evaluation
				require.NotNil(t, strategy.TokenExchange)
				assert.Equal(t, "CLIENT_SECRET", strategy.TokenExchange.ClientSecretEnv)
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
					Parameters: map[string]any{
						"properties": map[string]any{
							"param1": map[string]any{
								"type": "string",
							},
						},
						// Missing "type" at root level
					},
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			wantErr: true,
			errMsg:  "parameters must have 'type' field",
		},
		{
			name: "parameter type not string returns error",
			raw: []*rawCompositeTool{
				{
					Name: "bad",
					Parameters: map[string]any{
						"type": 123, // type must be string
						"properties": map[string]any{
							"param1": map[string]any{
								"type": "string",
							},
						},
					},
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			wantErr: true,
			errMsg:  "'type' field must be a string",
		},
		{
			name: "parameter type must be object returns error",
			raw: []*rawCompositeTool{
				{
					Name: "bad",
					Parameters: map[string]any{
						"type": "string", // must be "object" for parameter schemas
					},
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			wantErr: true,
			errMsg:  "'type' must be 'object'",
		},
		{
			name: "parameter with default value works",
			raw: []*rawCompositeTool{
				{
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
					Steps: []*rawWorkflowStep{{ID: "s1"}},
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				// Parameters is now map[string]any with JSON Schema format
				params := tools[0].Parameters
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

// TestYAMLLoader_transformOutputConfig tests the transformation of output configuration
// from raw YAML structures to the OutputConfig model.
func TestYAMLLoader_transformOutputConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     *rawOutputConfig
		verify  func(t *testing.T, cfg *OutputConfig)
		wantErr bool
		errMsg  string
	}{
		{
			name: "nil config returns nil",
			raw:  nil,
			verify: func(t *testing.T, cfg *OutputConfig) {
				t.Helper()
				assert.Nil(t, cfg)
			},
		},
		{
			name: "simple property with all fields",
			raw: &rawOutputConfig{
				Properties: map[string]rawOutputProperty{
					"message": {
						Type:        "string",
						Description: "Result message",
						Value:       "{{.steps.fetch.output.text}}",
						Default:     "default message",
					},
				},
				Required: []string{"message"},
			},
			verify: func(t *testing.T, cfg *OutputConfig) {
				t.Helper()
				require.NotNil(t, cfg)
				assert.Len(t, cfg.Properties, 1)
				assert.Equal(t, []string{"message"}, cfg.Required)

				msgProp, exists := cfg.Properties["message"]
				require.True(t, exists)
				assert.Equal(t, "string", msgProp.Type)
				assert.Equal(t, "Result message", msgProp.Description)
				assert.Equal(t, "{{.steps.fetch.output.text}}", msgProp.Value)
				assert.Equal(t, "default message", msgProp.Default)
			},
		},
		{
			name: "multiple properties with different types",
			raw: &rawOutputConfig{
				Properties: map[string]rawOutputProperty{
					"message": {
						Type:        "string",
						Description: "Result message",
						Value:       "{{.steps.fetch.output.text}}",
					},
					"count": {
						Type:        "integer",
						Description: "Item count",
						Value:       "{{.steps.fetch.output.count}}",
					},
					"success": {
						Type:        "boolean",
						Description: "Success flag",
						Value:       "{{.steps.fetch.output.success}}",
					},
					"score": {
						Type:        "number",
						Description: "Quality score",
						Value:       "{{.steps.fetch.output.score}}",
					},
				},
			},
			verify: func(t *testing.T, cfg *OutputConfig) {
				t.Helper()
				require.NotNil(t, cfg)
				assert.Len(t, cfg.Properties, 4)

				// Verify each property type
				for name, expectedType := range map[string]string{
					"message": "string",
					"count":   "integer",
					"success": "boolean",
					"score":   "number",
				} {
					prop, exists := cfg.Properties[name]
					require.True(t, exists, "property %s should exist", name)
					assert.Equal(t, expectedType, prop.Type, "property %s type mismatch", name)
				}
			},
		},
		{
			name: "nested object properties",
			raw: &rawOutputConfig{
				Properties: map[string]rawOutputProperty{
					"user": {
						Type:        "object",
						Description: "User information",
						Properties: map[string]rawOutputProperty{
							"id": {
								Type:        "string",
								Description: "User ID",
								Value:       "{{.steps.fetch_user.output.id}}",
							},
							"stats": {
								Type:        "object",
								Description: "User statistics",
								Properties: map[string]rawOutputProperty{
									"posts": {
										Type:        "integer",
										Description: "Number of posts",
										Value:       "{{.steps.fetch_user.output.post_count}}",
									},
								},
							},
						},
					},
				},
			},
			verify: func(t *testing.T, cfg *OutputConfig) {
				t.Helper()
				require.NotNil(t, cfg)

				userProp, exists := cfg.Properties["user"]
				require.True(t, exists)
				assert.Equal(t, "object", userProp.Type)

				// Verify second-level nested properties
				statsProp, exists := userProp.Properties["stats"]
				require.True(t, exists)
				assert.Equal(t, "object", statsProp.Type)

				postsProp, exists := statsProp.Properties["posts"]
				require.True(t, exists)
				assert.Equal(t, "integer", postsProp.Type)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			loader := &YAMLLoader{}
			cfg, err := loader.transformOutputConfig(tt.raw)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
				if tt.verify != nil {
					tt.verify(t, cfg)
				}
			}
		})
	}
}

// TestYAMLLoader_transformCompositeTools_WithOutputConfig tests that composite tools
// with output configurations are correctly parsed and transformed.
func TestYAMLLoader_transformCompositeTools_WithOutputConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     []*rawCompositeTool
		verify  func(t *testing.T, tools []*CompositeToolConfig)
		wantErr bool
		errMsg  string
	}{
		{
			name: "composite tool with simple output config",
			raw: []*rawCompositeTool{
				{
					Name:        "data_processor",
					Description: "Process data with typed output",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"source": map[string]any{"type": "string"},
						},
					},
					Steps: []*rawWorkflowStep{
						{ID: "fetch", Tool: "data.fetch"},
					},
					Output: &rawOutputConfig{
						Properties: map[string]rawOutputProperty{
							"message": {
								Type:        "string",
								Description: "Result message",
								Value:       "{{.steps.fetch.output.text}}",
							},
						},
						Required: []string{"message"},
					},
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				require.Len(t, tools, 1)
				tool := tools[0]

				assert.Equal(t, "data_processor", tool.Name)
				require.NotNil(t, tool.Output, "Output config should not be nil")
				assert.Len(t, tool.Output.Properties, 1)
				assert.Equal(t, []string{"message"}, tool.Output.Required)
			},
		},
		{
			name: "composite tool without output config (backward compatible)",
			raw: []*rawCompositeTool{
				{
					Name:        "simple_tool",
					Description: "Tool without output config",
					Steps:       []*rawWorkflowStep{{ID: "step1", Tool: "some.tool"}},
					Output:      nil,
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				require.Len(t, tools, 1)
				assert.Nil(t, tools[0].Output, "Output should be nil for backward compatibility")
			},
		},
		{
			name: "multiple composite tools with and without output configs",
			raw: []*rawCompositeTool{
				{
					Name:  "tool_with_output",
					Steps: []*rawWorkflowStep{{ID: "step1", Tool: "tool1"}},
					Output: &rawOutputConfig{
						Properties: map[string]rawOutputProperty{
							"result": {Type: "string", Value: "{{.steps.step1.output.text}}"},
						},
					},
				},
				{
					Name:   "tool_without_output",
					Steps:  []*rawWorkflowStep{{ID: "step2", Tool: "tool2"}},
					Output: nil,
				},
			},
			verify: func(t *testing.T, tools []*CompositeToolConfig) {
				t.Helper()
				require.Len(t, tools, 2)
				assert.NotNil(t, tools[0].Output)
				assert.Nil(t, tools[1].Output)
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

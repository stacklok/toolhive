// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func TestValidator_ValidateBasicFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid configuration",
			cfg: &Config{
				Name:  "test-vmcp",
				Group: "test-group",
				IncomingAuth: &IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
				Aggregation: &AggregationConfig{
					ConflictResolution: vmcp.ConflictStrategyPrefix,
					ConflictResolutionConfig: &ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing name",
			cfg: &Config{
				Group: "test-group",
				IncomingAuth: &IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
				Aggregation: &AggregationConfig{
					ConflictResolution: vmcp.ConflictStrategyPrefix,
					ConflictResolutionConfig: &ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "missing group reference",
			cfg: &Config{
				Name: "test-vmcp",
				IncomingAuth: &IncomingAuthConfig{
					Type: "anonymous",
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
				Aggregation: &AggregationConfig{
					ConflictResolution: vmcp.ConflictStrategyPrefix,
					ConflictResolutionConfig: &ConflictResolutionConfig{
						PrefixFormat: "{workload}_",
					},
				},
			},
			wantErr: true,
			errMsg:  "group reference is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.Validate(tt.cfg)

			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil {
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidator_ValidateIncomingAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		auth    *IncomingAuthConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid anonymous auth",
			auth: &IncomingAuthConfig{
				Type: "anonymous",
			},
			wantErr: false,
		},
		{
			name: "valid OIDC auth",
			auth: &IncomingAuthConfig{
				Type: "oidc",
				OIDC: &OIDCConfig{
					Issuer:          "https://example.com",
					ClientID:        "test-client",
					ClientSecretEnv: "OIDC_CLIENT_SECRET",
					Audience:        "vmcp",
					Scopes:          []string{"openid"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid OIDC auth without client secret (public client)",
			auth: &IncomingAuthConfig{
				Type: "oidc",
				OIDC: &OIDCConfig{
					Issuer:   "https://example.com",
					ClientID: "public-client",
					Audience: "vmcp",
				},
			},
			wantErr: false,
		},
		{
			name: "valid OIDC auth without client_id (JWT validation only)",
			auth: &IncomingAuthConfig{
				Type: "oidc",
				OIDC: &OIDCConfig{
					Issuer:   "https://example.com",
					Audience: "vmcp",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid auth type",
			auth: &IncomingAuthConfig{
				Type: "invalid",
			},
			wantErr: true,
			errMsg:  "incomingAuth.type must be one of",
		},
		{
			name: "OIDC without config",
			auth: &IncomingAuthConfig{
				Type: "oidc",
			},
			wantErr: true,
			errMsg:  "incomingAuth.oidc is required",
		},
		{
			name: "OIDC missing issuer",
			auth: &IncomingAuthConfig{
				Type: "oidc",
				OIDC: &OIDCConfig{
					ClientID:        "test-client",
					ClientSecretEnv: "OIDC_CLIENT_SECRET",
					Audience:        "vmcp",
				},
			},
			wantErr: true,
			errMsg:  "issuer is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.validateIncomingAuth(tt.auth)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateIncomingAuth() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateIncomingAuth() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidator_ValidateOutgoingAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		auth    *OutgoingAuthConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid inline source with unauthenticated default",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Default: &authtypes.BackendAuthStrategy{
					Type: "unauthenticated",
				},
			},
			wantErr: false,
		},
		{
			name: "valid headerInjection backend",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"github": {
						Type: "header_injection",
						HeaderInjection: &authtypes.HeaderInjectionConfig{
							HeaderName:  "Authorization",
							HeaderValue: "secret-token",
						},
					},
				},
			},
			wantErr: false,
		},
		// TODO: Uncomment when token_exchange strategy is implemented
		// {
		// 	name: "valid token_exchange backend",
		// 	auth: &OutgoingAuthConfig{
		// 		Source: "inline",
		// 		Backends: map[string]*authtypes.BackendAuthStrategy{
		// 			"github": {
		// 				Type: "token_exchange",
		// 				Metadata: map[string]any{
		// 					"token_url": "https://example.com/token",
		// 					"client_id": "test-client",
		// 					"audience":  "github-api",
		// 				},
		// 			},
		// 		},
		// 	},
		// 	wantErr: false,
		// },
		{
			name: "invalid source",
			auth: &OutgoingAuthConfig{
				Source: "invalid",
			},
			wantErr: true,
			errMsg:  "outgoingAuth.source must be one of",
		},
		{
			name: "invalid backend auth type",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"test": {
						Type: "invalid",
					},
				},
			},
			wantErr: true,
			errMsg:  "type must be one of",
		},
		{
			name: "valid upstream_inject backend",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"github": {
						Type: authtypes.StrategyTypeUpstreamInject,
						UpstreamInject: &authtypes.UpstreamInjectConfig{
							ProviderName: "github",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "upstream_inject nil config",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"github": {
						Type:           authtypes.StrategyTypeUpstreamInject,
						UpstreamInject: nil,
					},
				},
			},
			wantErr: true,
			errMsg:  "upstream_inject requires UpstreamInject configuration",
		},
		{
			name: "upstream_inject empty providerName allowed",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*authtypes.BackendAuthStrategy{
					"github": {
						Type: authtypes.StrategyTypeUpstreamInject,
						UpstreamInject: &authtypes.UpstreamInjectConfig{
							ProviderName: "",
						},
					},
				},
			},
			wantErr: false, // V-02 handles provider name resolution
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.validateOutgoingAuth(tt.auth)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateOutgoingAuth() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateOutgoingAuth() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidator_ValidateAggregation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		agg     *AggregationConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid prefix strategy",
			agg: &AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPrefix,
				ConflictResolutionConfig: &ConflictResolutionConfig{
					PrefixFormat: "{workload}_",
				},
			},
			wantErr: false,
		},
		{
			name: "valid priority strategy",
			agg: &AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPriority,
				ConflictResolutionConfig: &ConflictResolutionConfig{
					PriorityOrder: []string{"github", "jira"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid manual strategy",
			agg: &AggregationConfig{
				ConflictResolution:       vmcp.ConflictStrategyManual,
				ConflictResolutionConfig: &ConflictResolutionConfig{},
				Tools: []*WorkloadToolConfig{
					{
						Workload: "github",
						Overrides: map[string]*ToolOverride{
							"create_issue": {
								Name: "gh_create_issue",
							},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "prefix strategy missing format",
			agg: &AggregationConfig{
				ConflictResolution:       vmcp.ConflictStrategyPrefix,
				ConflictResolutionConfig: &ConflictResolutionConfig{},
			},
			wantErr: true,
			errMsg:  "prefixFormat is required",
		},
		{
			name: "priority strategy missing order",
			agg: &AggregationConfig{
				ConflictResolution:       vmcp.ConflictStrategyPriority,
				ConflictResolutionConfig: &ConflictResolutionConfig{},
			},
			wantErr: true,
			errMsg:  "priorityOrder is required",
		},
		{
			name: "manual strategy missing overrides",
			agg: &AggregationConfig{
				ConflictResolution:       vmcp.ConflictStrategyManual,
				ConflictResolutionConfig: &ConflictResolutionConfig{},
			},
			wantErr: true,
			errMsg:  "tool overrides are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.validateAggregation(tt.agg)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateAggregation() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateAggregation() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidator_ValidateCompositeTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		tools   []CompositeToolConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil tools (optional)",
			tools:   nil,
			wantErr: false,
		},
		{
			name: "valid composite tool",
			tools: []CompositeToolConfig{
				{
					Name:        "deploy_workflow",
					Description: "Deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:   "merge",
							Type: "tool",
							Tool: "github.merge_pr",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing tool name",
			tools: []CompositeToolConfig{
				{
					Description: "Deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:   "merge",
							Type: "tool",
							Tool: "github.merge_pr",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "duplicate tool name",
			tools: []CompositeToolConfig{
				{
					Name:        "deploy",
					Description: "Deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:   "merge",
							Type: "tool",
							Tool: "github.merge_pr",
						},
					},
				},
				{
					Name:        "deploy",
					Description: "Another deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:   "merge",
							Type: "tool",
							Tool: "jira.create_issue",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "duplicate composite tool name",
		},
		{
			name: "type inferred from tool field",
			tools: []CompositeToolConfig{
				{
					Name:        "fetch_data",
					Description: "Fetch data workflow",
					Timeout:     Duration(5 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:        "fetch",
							Type:      "tool", // Type would be inferred by loader from tool field
							Tool:      "backend.fetch",
							Arguments: thvjson.NewMap(map[string]any{"url": "https://example.com"}),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "timeout omitted uses default",
			tools: []CompositeToolConfig{
				{
					Name:        "no_timeout",
					Description: "Workflow without explicit timeout",
					Timeout:     0, // Omitted - should use default (30 minutes)
					Steps: []WorkflowStepConfig{
						{
							ID:   "step1",
							Type: "tool", // Type would be inferred by loader from tool field
							Tool: "backend.some_tool",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "elicitation step with explicit type",
			tools: []CompositeToolConfig{
				{
					Name:        "confirm_action",
					Description: "Confirmation workflow",
					Timeout:     Duration(5 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:      "confirm",
							Type:    "elicitation", // Explicit type
							Message: "Proceed?",
							Schema:  thvjson.NewMap(map[string]any{"type": "object"}),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing tool field when type defaults to tool",
			tools: []CompositeToolConfig{
				{
					Name:        "invalid_step",
					Description: "Invalid step workflow",
					Timeout:     Duration(5 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID: "step1",
							// No type (defaults to "tool"), no tool field
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "tool is required",
		},
		{
			name: "both tool and message fields present without explicit type",
			tools: []CompositeToolConfig{
				{
					Name:        "ambiguous_step",
					Description: "Step with both tool and message",
					Timeout:     Duration(5 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:      "step1",
							Tool:    "backend.some_tool", // Tool field present
							Message: "Some message",      // Message field also present
							// Type is missing - ambiguous configuration
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "cannot have both tool and message fields",
		},
		{
			name: "both tool and message fields present with explicit type",
			tools: []CompositeToolConfig{
				{
					Name:        "ambiguous_step",
					Description: "Step with both tool and message",
					Timeout:     Duration(5 * time.Minute),
					Steps: []WorkflowStepConfig{
						{
							ID:      "step1",
							Tool:    "backend.some_tool", // Tool field present
							Message: "Some message",      // Message field also present
							Type:    "tool",              // Explicit type resolves ambiguity
						},
					},
				},
			},
			wantErr: false, // Explicit type makes it unambiguous
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.validateCompositeTools(tt.tools)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateCompositeTools() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateCompositeTools() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidator_ValidateFailureHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fh      *FailureHandlingConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid configuration without circuit breaker",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				HealthCheckTimeout:  Duration(10 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
			},
			wantErr: false,
		},
		{
			name: "valid configuration with circuit breaker",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				HealthCheckTimeout:  Duration(10 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
				CircuitBreaker: &CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 5,
					Timeout:          Duration(60 * time.Second),
				},
			},
			wantErr: false,
		},
		{
			name: "valid configuration with circuit breaker disabled",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "bestEffort",
				CircuitBreaker: &CircuitBreakerConfig{
					Enabled: false,
				},
			},
			wantErr: false,
		},
		{
			name: "valid configuration with zero health check timeout (no timeout)",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				HealthCheckTimeout:  Duration(0),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
			},
			wantErr: false,
		},
		{
			name: "health check timeout >= interval",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				HealthCheckTimeout:  Duration(30 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
			},
			wantErr: true,
			errMsg:  "healthCheckTimeout (30s) must be less than healthCheckInterval (30s) to prevent checks from queuing up",
		},
		{
			name: "health check timeout > interval",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				HealthCheckTimeout:  Duration(35 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
			},
			wantErr: true,
			errMsg:  "healthCheckTimeout (35s) must be less than healthCheckInterval (30s) to prevent checks from queuing up",
		},
		{
			name: "negative health check timeout",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				HealthCheckTimeout:  Duration(-1 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
			},
			wantErr: true,
			errMsg:  "healthCheckTimeout must be >= 0 (zero means no timeout), got -1s",
		},
		{
			name: "circuit breaker failureThreshold < 1",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
				CircuitBreaker: &CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 0,
					Timeout:          Duration(60 * time.Second),
				},
			},
			wantErr: true,
			errMsg:  "circuitBreaker.failureThreshold must be >= 1, got 0",
		},
		{
			name: "circuit breaker timeout <= 0",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
				CircuitBreaker: &CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 5,
					Timeout:          Duration(0),
				},
			},
			wantErr: true,
			errMsg:  "circuitBreaker.timeout must be > 0, got 0s",
		},
		{
			name: "circuit breaker timeout < 1s",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
				CircuitBreaker: &CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 5,
					Timeout:          Duration(500 * time.Millisecond),
				},
			},
			wantErr: true,
			errMsg:  "circuitBreaker.timeout must be >= 1s to prevent thrashing, got 500ms",
		},
		{
			name: "invalid partial failure mode",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "invalid",
			},
			wantErr: true,
			errMsg:  "partialFailureMode must be one of: fail, bestEffort",
		},
		{
			name: "negative health check interval",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(-1 * time.Second),
				UnhealthyThreshold:  3,
				PartialFailureMode:  "fail",
			},
			wantErr: true,
			errMsg:  "healthCheckInterval must be positive",
		},
		{
			name: "negative unhealthy threshold",
			fh: &FailureHandlingConfig{
				HealthCheckInterval: Duration(30 * time.Second),
				UnhealthyThreshold:  -1,
				PartialFailureMode:  "fail",
			},
			wantErr: true,
			errMsg:  "unhealthyThreshold must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.validateFailureHandling(tt.fh)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateFailureHandling() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateFailureHandling() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidateAuthServerIntegration(t *testing.T) {
	t.Parallel()

	// Helper to build a minimal valid auth server RunConfig.
	validASRunConfig := func(issuer string, upstreamName string) *authserver.RunConfig {
		return &authserver.RunConfig{
			Issuer: issuer,
			Upstreams: []authserver.UpstreamRunConfig{
				{Name: upstreamName, Type: authserver.UpstreamProviderTypeOIDC},
			},
			AllowedAudiences: []string{"https://my-vmcp"},
		}
	}

	tests := []struct {
		name    string
		cfg     *Config
		rc      *authserver.RunConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "mode_a_no_auth_server_passes",
			cfg: &Config{
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Default: &authtypes.BackendAuthStrategy{
						Type: authtypes.StrategyTypeUnauthenticated,
					},
				},
			},
			rc:      nil,
			wantErr: false,
		},
		{
			name: "v01_upstream_inject_without_auth_server",
			cfg: &Config{
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Backends: map[string]*authtypes.BackendAuthStrategy{
						"github-tools": {
							Type: authtypes.StrategyTypeUpstreamInject,
							UpstreamInject: &authtypes.UpstreamInjectConfig{
								ProviderName: "github",
							},
						},
					},
				},
			},
			rc:      nil,
			wantErr: true,
			errMsg:  "upstream_inject requires an embedded auth server",
		},
		{
			name: "v02_provider_not_in_upstreams",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Backends: map[string]*authtypes.BackendAuthStrategy{
						"github-tools": {
							Type: authtypes.StrategyTypeUpstreamInject,
							UpstreamInject: &authtypes.UpstreamInjectConfig{
								ProviderName: "github",
							},
						},
					},
				},
			},
			rc: &authserver.RunConfig{
				Issuer: "http://localhost:9090",
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "entra", Type: authserver.UpstreamProviderTypeOIDC},
				},
				AllowedAudiences: []string{"https://my-vmcp"},
			},
			wantErr: true,
			errMsg:  "not found in auth server upstreams",
		},
		{
			name: "v04_issuer_mismatch",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:8080",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
			},
			rc:      validASRunConfig("http://localhost:9090", "default"),
			wantErr: true,
			errMsg:  "issuer mismatch",
		},
		{
			name: "v05_empty_issuer",
			cfg: &Config{
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
			},
			rc: &authserver.RunConfig{
				Issuer: "",
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "default", Type: authserver.UpstreamProviderTypeOIDC},
				},
			},
			wantErr: true,
			errMsg:  "issuer is required",
		},
		{
			name: "v05_no_upstreams",
			cfg: &Config{
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
			},
			rc: &authserver.RunConfig{
				Issuer:    "http://localhost:9090",
				Upstreams: nil,
			},
			wantErr: true,
			errMsg:  "at least one upstream",
		},
		{
			name: "v07_audience_not_in_allowed",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-app",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
			},
			rc: &authserver.RunConfig{
				Issuer: "http://localhost:9090",
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "default", Type: authserver.UpstreamProviderTypeOIDC},
				},
				AllowedAudiences: []string{"https://other"},
			},
			wantErr: true,
			errMsg:  "not in auth server's allowed audiences",
		},
		{
			name: "v09_auth_server_requires_oidc_incoming",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeAnonymous,
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
			},
			rc:      validASRunConfig("http://localhost:9090", "default"),
			wantErr: true,
			errMsg:  "embedded auth server requires OIDC incoming auth",
		},
		{
			name: "v13_empty_allowed_audiences",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
				},
			},
			rc: &authserver.RunConfig{
				Issuer: "http://localhost:9090",
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "default", Type: authserver.UpstreamProviderTypeOIDC},
				},
				AllowedAudiences: nil,
			},
			wantErr: true,
			errMsg:  "at least one allowed audience",
		},
		{
			name: "v02_empty_upstream_name_matches_default",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Backends: map[string]*authtypes.BackendAuthStrategy{
						"my-backend": {
							Type: authtypes.StrategyTypeUpstreamInject,
							UpstreamInject: &authtypes.UpstreamInjectConfig{
								ProviderName: "default",
							},
						},
					},
				},
			},
			rc: &authserver.RunConfig{
				Issuer: "http://localhost:9090",
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "", Type: authserver.UpstreamProviderTypeOIDC}, // empty name → "default"
				},
				AllowedAudiences: []string{"https://my-vmcp"},
			},
			wantErr: false,
		},
		{
			name: "upstream_inject_as_default_strategy",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Default: &authtypes.BackendAuthStrategy{
						Type: authtypes.StrategyTypeUpstreamInject,
						UpstreamInject: &authtypes.UpstreamInjectConfig{
							ProviderName: "github",
						},
					},
				},
			},
			rc:      validASRunConfig("http://localhost:9090", "github"),
			wantErr: false,
		},
		{
			name: "upstream_inject_default_provider_not_found",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Default: &authtypes.BackendAuthStrategy{
						Type: authtypes.StrategyTypeUpstreamInject,
						UpstreamInject: &authtypes.UpstreamInjectConfig{
							ProviderName: "nonexistent",
						},
					},
				},
			},
			rc:      validASRunConfig("http://localhost:9090", "github"),
			wantErr: true,
			errMsg:  "not found in auth server upstreams",
		},
		{
			name: "valid_mode_b_config",
			cfg: &Config{
				IncomingAuth: &IncomingAuthConfig{
					Type: IncomingAuthTypeOIDC,
					OIDC: &OIDCConfig{
						Issuer:   "http://localhost:9090",
						Audience: "https://my-vmcp",
					},
				},
				OutgoingAuth: &OutgoingAuthConfig{
					Source: "inline",
					Backends: map[string]*authtypes.BackendAuthStrategy{
						"github-tools": {
							Type: authtypes.StrategyTypeUpstreamInject,
							UpstreamInject: &authtypes.UpstreamInjectConfig{
								ProviderName: "github",
							},
						},
					},
				},
			},
			rc: &authserver.RunConfig{
				Issuer: "http://localhost:9090",
				Upstreams: []authserver.UpstreamRunConfig{
					{Name: "github", Type: authserver.UpstreamProviderTypeOIDC},
				},
				AllowedAudiences: []string{"https://my-vmcp"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAuthServerIntegration(tt.cfg, tt.rc)

			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAuthServerIntegration() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateAuthServerIntegration() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
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
			name: "invalid auth type",
			auth: &IncomingAuthConfig{
				Type: "invalid",
			},
			wantErr: true,
			errMsg:  "incoming_auth.type must be one of",
		},
		{
			name: "OIDC without config",
			auth: &IncomingAuthConfig{
				Type: "oidc",
			},
			wantErr: true,
			errMsg:  "incoming_auth.oidc is required",
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
				Default: &BackendAuthStrategy{
					Type: "unauthenticated",
				},
			},
			wantErr: false,
		},
		{
			name: "valid header_injection backend",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*BackendAuthStrategy{
					"github": {
						Type: "header_injection",
						Metadata: map[string]any{
							"header_name":  "Authorization",
							"header_value": "secret-token",
						},
					},
				},
			},
			wantErr: false,
		},
		// TODO: Uncomment when pass_through strategy is implemented
		// {
		// 	name: "valid inline source with pass_through default",
		// 	auth: &OutgoingAuthConfig{
		// 		Source: "inline",
		// 		Default: &BackendAuthStrategy{
		// 			Type: "pass_through",
		// 		},
		// 	},
		// 	wantErr: false,
		// },
		// TODO: Uncomment when token_exchange strategy is implemented
		// {
		// 	name: "valid token_exchange backend",
		// 	auth: &OutgoingAuthConfig{
		// 		Source: "inline",
		// 		Backends: map[string]*BackendAuthStrategy{
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
			errMsg:  "outgoing_auth.source must be one of",
		},
		{
			name: "invalid backend auth type",
			auth: &OutgoingAuthConfig{
				Source: "inline",
				Backends: map[string]*BackendAuthStrategy{
					"test": {
						Type: "invalid",
					},
				},
			},
			wantErr: true,
			errMsg:  "type must be one of",
		},
		// TODO: Uncomment when token_exchange strategy is implemented
		// {
		// 	name: "token_exchange missing required metadata",
		// 	auth: &OutgoingAuthConfig{
		// 		Source: "inline",
		// 		Backends: map[string]*BackendAuthStrategy{
		// 			"github": {
		// 				Type: "token_exchange",
		// 				Metadata: map[string]any{
		// 					"client_id": "test-client",
		// 					// Missing token_url and audience
		// 				},
		// 			},
		// 		},
		// 	},
		// 	wantErr: true,
		// 	errMsg:  "token_exchange requires metadata field",
		// },
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
			errMsg:  "prefix_format is required",
		},
		{
			name: "priority strategy missing order",
			agg: &AggregationConfig{
				ConflictResolution:       vmcp.ConflictStrategyPriority,
				ConflictResolutionConfig: &ConflictResolutionConfig{},
			},
			wantErr: true,
			errMsg:  "priority_order is required",
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

func TestValidator_ValidateTokenCache(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cache   *TokenCacheConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil cache (optional)",
			cache:   nil,
			wantErr: false,
		},
		{
			name: "valid memory cache",
			cache: &TokenCacheConfig{
				Provider: CacheProviderMemory,
				Memory: &MemoryCacheConfig{
					MaxEntries: 1000,
					TTLOffset:  Duration(5 * time.Minute),
				},
			},
			wantErr: false,
		},
		{
			name: "valid redis cache",
			cache: &TokenCacheConfig{
				Provider: "redis",
				Redis: &RedisCacheConfig{
					Address:   "localhost:6379",
					TTLOffset: Duration(5 * time.Minute),
				},
			},
			wantErr: false,
		},
		{
			name: "invalid provider",
			cache: &TokenCacheConfig{
				Provider: "invalid",
			},
			wantErr: true,
			errMsg:  "token_cache.provider must be one of",
		},
		{
			name: "memory cache with negative max entries",
			cache: &TokenCacheConfig{
				Provider: CacheProviderMemory,
				Memory: &MemoryCacheConfig{
					MaxEntries: -1,
				},
			},
			wantErr: true,
			errMsg:  "max_entries must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator()
			err := v.validateTokenCache(tt.cache)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateTokenCache() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateTokenCache() error message = %v, want to contain %v", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestValidator_ValidateCompositeTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		tools   []*CompositeToolConfig
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
			tools: []*CompositeToolConfig{
				{
					Name:        "deploy_workflow",
					Description: "Deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []*WorkflowStepConfig{
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
			tools: []*CompositeToolConfig{
				{
					Description: "Deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []*WorkflowStepConfig{
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
			tools: []*CompositeToolConfig{
				{
					Name:        "deploy",
					Description: "Deploy workflow",
					Timeout:     Duration(30 * time.Minute),
					Steps: []*WorkflowStepConfig{
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
					Steps: []*WorkflowStepConfig{
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

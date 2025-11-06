// Package config provides the configuration model for Virtual MCP Server.
//
// This package defines a platform-agnostic configuration model that works
// for both CLI (YAML) and Kubernetes (CRD) deployments. Platform-specific
// adapters transform their native formats into this unified model.
package config

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Token cache provider types
const (
	// CacheProviderMemory represents in-memory token cache provider
	CacheProviderMemory = "memory"
	// CacheProviderRedis represents Redis token cache provider
	CacheProviderRedis = "redis"
)

// Duration is a wrapper around time.Duration that marshals/unmarshals as a duration string.
// This ensures duration values are serialized as "30s", "1m", etc. instead of nanosecond integers.
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = Duration(dur)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = Duration(dur)
	return nil
}

// Config is the unified configuration model for Virtual MCP Server.
// This is platform-agnostic and used by both CLI and Kubernetes deployments.
//
// Platform-specific adapters (CLI YAML loader, Kubernetes CRD converter)
// transform their native formats into this model.
type Config struct {
	// Name is the virtual MCP server name.
	Name string `json:"name" yaml:"name"`

	// Group references the ToolHive group containing backend workloads.
	Group string `json:"group_ref" yaml:"group"`

	// IncomingAuth configures how clients authenticate to the virtual MCP server.
	IncomingAuth *IncomingAuthConfig `json:"incoming_auth,omitempty" yaml:"incoming_auth,omitempty"`

	// OutgoingAuth configures how the virtual MCP server authenticates to backends.
	OutgoingAuth *OutgoingAuthConfig `json:"outgoing_auth,omitempty" yaml:"outgoing_auth,omitempty"`

	// Aggregation configures capability aggregation and conflict resolution.
	Aggregation *AggregationConfig `json:"aggregation,omitempty" yaml:"aggregation,omitempty"`

	// CompositeTools defines inline composite tool workflows.
	// Full workflow definitions are embedded in the configuration.
	// For Kubernetes, complex workflows can also reference VirtualMCPCompositeToolDefinition CRDs.
	CompositeTools []*CompositeToolConfig `json:"composite_tools,omitempty" yaml:"composite_tools,omitempty"`

	// TokenCache configures token caching.
	TokenCache *TokenCacheConfig `json:"token_cache,omitempty" yaml:"token_cache,omitempty"`

	// Operational configures operational settings.
	Operational *OperationalConfig `json:"operational,omitempty" yaml:"operational,omitempty"`

	// Metadata stores additional configuration metadata.
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// IncomingAuthConfig configures client authentication to the virtual MCP server.
type IncomingAuthConfig struct {
	// Type is the auth type: "oidc", "local", "anonymous"
	Type string `json:"type" yaml:"type"`

	// OIDC contains OIDC configuration (when Type = "oidc").
	OIDC *OIDCConfig `json:"oidc,omitempty" yaml:"oidc,omitempty"`

	// Authz contains authorization configuration (optional).
	Authz *AuthzConfig `json:"authz,omitempty" yaml:"authz,omitempty"`
}

// OIDCConfig configures OpenID Connect authentication.
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL.
	Issuer string `json:"issuer" yaml:"issuer"`

	// ClientID is the OAuth client ID.
	ClientID string `json:"client_id" yaml:"client_id"`

	// ClientSecretEnv is the name of the environment variable containing the client secret.
	// This is the secure way to reference secrets - the actual secret value is never stored
	// in configuration files, only the environment variable name.
	// The secret value will be resolved from this environment variable at runtime.
	ClientSecretEnv string `json:"client_secret_env,omitempty" yaml:"client_secret_env,omitempty"`

	// Audience is the required token audience.
	Audience string `json:"audience" yaml:"audience"`

	// Resource is the OAuth 2.0 resource indicator (RFC 8707).
	// Used in WWW-Authenticate header and OAuth discovery metadata (RFC 9728).
	// If not specified, defaults to Audience.
	Resource string `json:"resource,omitempty" yaml:"resource,omitempty"`

	// Scopes are the required OAuth scopes.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// AuthzConfig configures authorization.
type AuthzConfig struct {
	// Type is the authz type: "cedar", "none"
	Type string `json:"type" yaml:"type"`

	// Policies contains Cedar policy definitions (when Type = "cedar").
	Policies []string `json:"policies,omitempty" yaml:"policies,omitempty"`
}

// OutgoingAuthConfig configures backend authentication.
type OutgoingAuthConfig struct {
	// Source defines how to discover backend auth: "inline", "discovered", "mixed"
	// - inline: Explicit configuration in OutgoingAuth
	// - discovered: Auto-discover from backend MCPServer.externalAuthConfigRef (Kubernetes only)
	// - mixed: Discover with selective overrides
	Source string `json:"source" yaml:"source"`

	// Default is the default auth strategy for backends without explicit config.
	Default *BackendAuthStrategy `json:"default,omitempty" yaml:"default,omitempty"`

	// Backends contains per-backend auth configuration.
	Backends map[string]*BackendAuthStrategy `json:"backends,omitempty" yaml:"backends,omitempty"`
}

// BackendAuthStrategy defines how to authenticate to a specific backend.
type BackendAuthStrategy struct {
	// Type is the auth strategy: "pass_through", "token_exchange", "client_credentials",
	// "service_account", "header_injection", "oauth_proxy"
	Type string `json:"type" yaml:"type"`

	// Metadata contains strategy-specific configuration.
	// This is opaque and interpreted by the auth strategy implementation.
	Metadata map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// AggregationConfig configures capability aggregation.
type AggregationConfig struct {
	// ConflictResolution is the strategy: "prefix", "priority", "manual"
	ConflictResolution vmcp.ConflictResolutionStrategy `json:"conflict_resolution" yaml:"conflict_resolution"`

	// ConflictResolutionConfig contains strategy-specific configuration.
	ConflictResolutionConfig *ConflictResolutionConfig `json:"conflict_resolution_config,omitempty" yaml:"conflict_resolution_config,omitempty"` //nolint:lll

	// Tools contains per-workload tool configuration.
	Tools []*WorkloadToolConfig `json:"tools,omitempty" yaml:"tools,omitempty"`
}

// ConflictResolutionConfig contains conflict resolution settings.
type ConflictResolutionConfig struct {
	// PrefixFormat is the prefix format (for prefix strategy).
	// Options: "{workload}", "{workload}_", "{workload}.", custom string
	PrefixFormat string `json:"prefix_format,omitempty" yaml:"prefix_format,omitempty"`

	// PriorityOrder is the explicit priority ordering (for priority strategy).
	PriorityOrder []string `json:"priority_order,omitempty" yaml:"priority_order,omitempty"`
}

// WorkloadToolConfig configures tool filtering/overrides for a workload.
type WorkloadToolConfig struct {
	// Workload is the workload name/ID.
	Workload string `json:"workload" yaml:"workload"`

	// Filter is the list of tools to include (nil = include all).
	Filter []string `json:"filter,omitempty" yaml:"filter,omitempty"`

	// Overrides maps tool names to override configurations.
	Overrides map[string]*ToolOverride `json:"overrides,omitempty" yaml:"overrides,omitempty"`
}

// ToolOverride defines tool name/description overrides.
type ToolOverride struct {
	// Name is the new tool name (for renaming).
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is the new tool description (for updating).
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// TokenCacheConfig configures token caching.
type TokenCacheConfig struct {
	// Provider is the cache provider: "memory", "redis"
	Provider string `json:"provider" yaml:"provider"`

	// Memory contains memory cache config (when Provider = "memory").
	Memory *MemoryCacheConfig `json:"memory,omitempty" yaml:"memory,omitempty"`

	// Redis contains Redis cache config (when Provider = "redis").
	Redis *RedisCacheConfig `json:"redis,omitempty" yaml:"redis,omitempty"`
}

// MemoryCacheConfig configures in-memory token caching.
type MemoryCacheConfig struct {
	// MaxEntries is the maximum number of cached tokens.
	MaxEntries int `json:"max_entries" yaml:"max_entries"`

	// TTLOffset is how long before expiry to refresh tokens.
	TTLOffset Duration `json:"ttl_offset" yaml:"ttl_offset"`
}

// RedisCacheConfig configures Redis token caching.
type RedisCacheConfig struct {
	// Address is the Redis server address.
	Address string `json:"address" yaml:"address"`

	// DB is the Redis database number.
	DB int `json:"db" yaml:"db"`

	// KeyPrefix is the prefix for cache keys.
	KeyPrefix string `json:"key_prefix,omitempty" yaml:"key_prefix,omitempty"`

	// Password is the Redis password (or secret reference).
	Password string `json:"password,omitempty" yaml:"password,omitempty"`

	// TTLOffset is how long before expiry to refresh tokens.
	TTLOffset Duration `json:"ttl_offset" yaml:"ttl_offset"`
}

// OperationalConfig contains operational settings.
type OperationalConfig struct {
	// Timeouts configures request timeouts.
	Timeouts *TimeoutConfig `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`

	// FailureHandling configures failure handling.
	FailureHandling *FailureHandlingConfig `json:"failure_handling,omitempty" yaml:"failure_handling,omitempty"`
}

// TimeoutConfig configures timeouts.
type TimeoutConfig struct {
	// Default is the default timeout for backend requests.
	Default Duration `json:"default" yaml:"default"`

	// PerWorkload contains per-workload timeout overrides.
	PerWorkload map[string]Duration `json:"per_workload,omitempty" yaml:"per_workload,omitempty"`
}

// FailureHandlingConfig configures failure handling.
type FailureHandlingConfig struct {
	// HealthCheckInterval is how often to check backend health.
	HealthCheckInterval Duration `json:"health_check_interval" yaml:"health_check_interval"`

	// UnhealthyThreshold is how many failures before marking unhealthy.
	UnhealthyThreshold int `json:"unhealthy_threshold" yaml:"unhealthy_threshold"`

	// PartialFailureMode defines behavior when some backends fail.
	// Options: "fail" (fail entire request), "best_effort" (return partial results)
	PartialFailureMode string `json:"partial_failure_mode" yaml:"partial_failure_mode"`

	// CircuitBreaker configures circuit breaker settings.
	CircuitBreaker *CircuitBreakerConfig `json:"circuit_breaker,omitempty" yaml:"circuit_breaker,omitempty"`
}

// CircuitBreakerConfig configures circuit breaker.
type CircuitBreakerConfig struct {
	// Enabled indicates if circuit breaker is enabled.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// FailureThreshold is how many failures trigger open circuit.
	FailureThreshold int `json:"failure_threshold" yaml:"failure_threshold"`

	// Timeout is how long to keep circuit open.
	Timeout Duration `json:"timeout" yaml:"timeout"`
}

// CompositeToolConfig defines a composite tool workflow.
// This matches the YAML structure from the proposal (lines 173-255).
type CompositeToolConfig struct {
	// Name is the workflow name (unique identifier).
	Name string `json:"name"`

	// Description describes what the workflow does.
	Description string `json:"description,omitempty"`

	// Parameters defines input parameter schema.
	Parameters map[string]ParameterSchema `json:"parameters,omitempty"`

	// Timeout is the maximum workflow execution time.
	Timeout Duration `json:"timeout,omitempty"`

	// Steps are the workflow steps to execute.
	Steps []*WorkflowStepConfig `json:"steps"`
}

// ParameterSchema defines a workflow parameter.
type ParameterSchema struct {
	// Type is the parameter type (e.g., "string", "integer").
	Type string `json:"type"`

	// Default is the default value (optional).
	Default any `json:"default,omitempty"`
}

// WorkflowStepConfig defines a single workflow step.
// This matches the proposal's step configuration (lines 180-255).
type WorkflowStepConfig struct {
	// ID uniquely identifies this step.
	ID string `json:"id"`

	// Type is the step type: "tool", "elicitation"
	Type string `json:"type"`

	// Tool is the tool name to call (for tool steps).
	Tool string `json:"tool,omitempty"`

	// Arguments are the tool arguments (supports template expansion).
	Arguments map[string]any `json:"arguments,omitempty"`

	// Condition is an optional execution condition (template syntax).
	Condition string `json:"condition,omitempty"`

	// DependsOn lists step IDs that must complete first (for DAG execution).
	DependsOn []string `json:"depends_on,omitempty"`

	// OnError defines error handling for this step.
	OnError *StepErrorHandling `json:"on_error,omitempty"`

	// Elicitation config (for elicitation steps).
	Message string         `json:"message,omitempty"` // Elicitation message
	Schema  map[string]any `json:"schema,omitempty"`  // JSON Schema for requested data
	Timeout Duration       `json:"timeout,omitempty"` // Elicitation timeout

	// Elicitation response handlers.
	OnDecline *ElicitationResponseConfig `json:"on_decline,omitempty"`
	OnCancel  *ElicitationResponseConfig `json:"on_cancel,omitempty"`
}

// StepErrorHandling defines error handling for a workflow step.
type StepErrorHandling struct {
	// Action: "abort", "continue", "retry"
	Action string `json:"action"`

	// RetryCount is the number of retry attempts (for retry action).
	RetryCount int `json:"retry_count,omitempty"`

	// RetryDelay is the initial delay between retries.
	RetryDelay Duration `json:"retry_delay,omitempty"`
}

// ElicitationResponseConfig defines how to handle elicitation responses.
type ElicitationResponseConfig struct {
	// Action: "skip_remaining", "abort", "continue"
	Action string `json:"action"`
}

// Validator validates configuration.
type Validator interface {
	// Validate checks if the configuration is valid.
	// Returns detailed validation errors.
	Validate(cfg *Config) error
}

// Loader loads configuration from a source.
type Loader interface {
	// Load loads configuration from the source.
	Load() (*Config, error)
}

// Package config provides the configuration model for Virtual MCP Server.
//
// This package defines a platform-agnostic configuration model that works
// for both CLI (YAML) and Kubernetes (CRD) deployments. Platform-specific
// adapters transform their native formats into this unified model.
package config

import (
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

// Config is the unified configuration model for Virtual MCP Server.
// This is platform-agnostic and used by both CLI and Kubernetes deployments.
//
// Platform-specific adapters (CLI YAML loader, Kubernetes CRD converter)
// transform their native formats into this model.
type Config struct {
	// Name is the virtual MCP server name.
	Name string

	// GroupRef references the ToolHive group containing backend workloads.
	GroupRef string

	// IncomingAuth configures how clients authenticate to the virtual MCP server.
	IncomingAuth *IncomingAuthConfig

	// OutgoingAuth configures how the virtual MCP server authenticates to backends.
	OutgoingAuth *OutgoingAuthConfig

	// Aggregation configures capability aggregation and conflict resolution.
	Aggregation *AggregationConfig

	// CompositeTools defines inline composite tool workflows.
	// Full workflow definitions are embedded in the configuration.
	// For Kubernetes, complex workflows can also reference VirtualMCPCompositeToolDefinition CRDs.
	CompositeTools []*CompositeToolConfig

	// TokenCache configures token caching.
	TokenCache *TokenCacheConfig

	// Operational configures operational settings.
	Operational *OperationalConfig

	// Metadata stores additional configuration metadata.
	Metadata map[string]string
}

// IncomingAuthConfig configures client authentication to the virtual MCP server.
type IncomingAuthConfig struct {
	// Type is the auth type: "oidc", "local", "anonymous"
	Type string

	// OIDC contains OIDC configuration (when Type = "oidc").
	OIDC *OIDCConfig

	// Authz contains authorization configuration (optional).
	Authz *AuthzConfig
}

// OIDCConfig configures OpenID Connect authentication.
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL.
	Issuer string

	// ClientID is the OAuth client ID.
	ClientID string

	// ClientSecret is the OAuth client secret (or secret reference).
	ClientSecret string

	// Audience is the required token audience.
	Audience string

	// Scopes are the required OAuth scopes.
	Scopes []string
}

// AuthzConfig configures authorization.
type AuthzConfig struct {
	// Type is the authz type: "cedar", "none"
	Type string

	// Policies contains Cedar policy definitions (when Type = "cedar").
	Policies []string
}

// OutgoingAuthConfig configures backend authentication.
type OutgoingAuthConfig struct {
	// Source defines how to discover backend auth: "inline", "discovered", "mixed"
	// - inline: Explicit configuration in OutgoingAuth
	// - discovered: Auto-discover from backend MCPServer.externalAuthConfigRef (Kubernetes only)
	// - mixed: Discover with selective overrides
	Source string

	// Default is the default auth strategy for backends without explicit config.
	Default *BackendAuthStrategy

	// Backends contains per-backend auth configuration.
	Backends map[string]*BackendAuthStrategy
}

// BackendAuthStrategy defines how to authenticate to a specific backend.
type BackendAuthStrategy struct {
	// Type is the auth strategy: "pass_through", "token_exchange", "client_credentials",
	// "service_account", "header_injection", "oauth_proxy"
	Type string

	// Metadata contains strategy-specific configuration.
	// This is opaque and interpreted by the auth strategy implementation.
	Metadata map[string]any
}

// AggregationConfig configures capability aggregation.
type AggregationConfig struct {
	// ConflictResolution is the strategy: "prefix", "priority", "manual"
	ConflictResolution vmcp.ConflictResolutionStrategy

	// ConflictResolutionConfig contains strategy-specific configuration.
	ConflictResolutionConfig *ConflictResolutionConfig

	// Tools contains per-workload tool configuration.
	Tools []*WorkloadToolConfig
}

// ConflictResolutionConfig contains conflict resolution settings.
type ConflictResolutionConfig struct {
	// PrefixFormat is the prefix format (for prefix strategy).
	// Options: "{workload}", "{workload}_", "{workload}.", custom string
	PrefixFormat string

	// PriorityOrder is the explicit priority ordering (for priority strategy).
	PriorityOrder []string
}

// WorkloadToolConfig configures tool filtering/overrides for a workload.
type WorkloadToolConfig struct {
	// Workload is the workload name/ID.
	Workload string

	// Filter is the list of tools to include (nil = include all).
	Filter []string

	// Overrides maps tool names to override configurations.
	Overrides map[string]*ToolOverride
}

// ToolOverride defines tool name/description overrides.
type ToolOverride struct {
	// Name is the new tool name (for renaming).
	Name string

	// Description is the new tool description (for updating).
	Description string
}

// TokenCacheConfig configures token caching.
type TokenCacheConfig struct {
	// Provider is the cache provider: "memory", "redis"
	Provider string

	// Memory contains memory cache config (when Provider = "memory").
	Memory *MemoryCacheConfig

	// Redis contains Redis cache config (when Provider = "redis").
	Redis *RedisCacheConfig
}

// MemoryCacheConfig configures in-memory token caching.
type MemoryCacheConfig struct {
	// MaxEntries is the maximum number of cached tokens.
	MaxEntries int

	// TTLOffset is how long before expiry to refresh tokens.
	TTLOffset time.Duration
}

// RedisCacheConfig configures Redis token caching.
type RedisCacheConfig struct {
	// Address is the Redis server address.
	Address string

	// DB is the Redis database number.
	DB int

	// KeyPrefix is the prefix for cache keys.
	KeyPrefix string

	// Password is the Redis password (or secret reference).
	Password string

	// TTLOffset is how long before expiry to refresh tokens.
	TTLOffset time.Duration
}

// OperationalConfig contains operational settings.
type OperationalConfig struct {
	// Timeouts configures request timeouts.
	Timeouts *TimeoutConfig

	// FailureHandling configures failure handling.
	FailureHandling *FailureHandlingConfig
}

// TimeoutConfig configures timeouts.
type TimeoutConfig struct {
	// Default is the default timeout for backend requests.
	Default time.Duration

	// PerWorkload contains per-workload timeout overrides.
	PerWorkload map[string]time.Duration
}

// FailureHandlingConfig configures failure handling.
type FailureHandlingConfig struct {
	// HealthCheckInterval is how often to check backend health.
	HealthCheckInterval time.Duration

	// UnhealthyThreshold is how many failures before marking unhealthy.
	UnhealthyThreshold int

	// PartialFailureMode defines behavior when some backends fail.
	// Options: "fail" (fail entire request), "best_effort" (return partial results)
	PartialFailureMode string

	// CircuitBreaker configures circuit breaker settings.
	CircuitBreaker *CircuitBreakerConfig
}

// CircuitBreakerConfig configures circuit breaker.
type CircuitBreakerConfig struct {
	// Enabled indicates if circuit breaker is enabled.
	Enabled bool

	// FailureThreshold is how many failures trigger open circuit.
	FailureThreshold int

	// Timeout is how long to keep circuit open.
	Timeout time.Duration
}

// CompositeToolConfig defines a composite tool workflow.
// This matches the YAML structure from the proposal (lines 173-255).
type CompositeToolConfig struct {
	// Name is the workflow name (unique identifier).
	Name string

	// Description describes what the workflow does.
	Description string

	// Parameters defines input parameter schema.
	Parameters map[string]ParameterSchema

	// Timeout is the maximum workflow execution time.
	Timeout time.Duration

	// Steps are the workflow steps to execute.
	Steps []*WorkflowStepConfig
}

// ParameterSchema defines a workflow parameter.
type ParameterSchema struct {
	// Type is the parameter type (e.g., "string", "integer").
	Type string

	// Default is the default value (optional).
	Default any
}

// WorkflowStepConfig defines a single workflow step.
// This matches the proposal's step configuration (lines 180-255).
type WorkflowStepConfig struct {
	// ID uniquely identifies this step.
	ID string

	// Type is the step type: "tool", "elicitation"
	Type string

	// Tool is the tool name to call (for tool steps).
	Tool string

	// Arguments are the tool arguments (supports template expansion).
	Arguments map[string]any

	// Condition is an optional execution condition (template syntax).
	Condition string

	// DependsOn lists step IDs that must complete first (for DAG execution).
	DependsOn []string

	// OnError defines error handling for this step.
	OnError *StepErrorHandling

	// Elicitation config (for elicitation steps).
	Message string         // Elicitation message
	Schema  map[string]any // JSON Schema for requested data
	Timeout time.Duration  // Elicitation timeout

	// Elicitation response handlers.
	OnDecline *ElicitationResponseConfig
	OnCancel  *ElicitationResponseConfig
}

// StepErrorHandling defines error handling for a workflow step.
type StepErrorHandling struct {
	// Action: "abort", "continue", "retry"
	Action string

	// RetryCount is the number of retry attempts (for retry action).
	RetryCount int

	// RetryDelay is the initial delay between retries.
	RetryDelay time.Duration
}

// ElicitationResponseConfig defines how to handle elicitation responses.
type ElicitationResponseConfig struct {
	// Action: "skip_remaining", "abort", "continue"
	Action string
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

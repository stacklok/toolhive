package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
)

// YAMLLoader loads configuration from a YAML file.
// This is the CLI-specific loader that parses the YAML format defined in the proposal.
type YAMLLoader struct {
	filePath  string
	envReader env.Reader
}

// NewYAMLLoader creates a new YAML configuration loader.
func NewYAMLLoader(filePath string, envReader env.Reader) *YAMLLoader {
	return &YAMLLoader{
		filePath:  filePath,
		envReader: envReader,
	}
}

// Load reads and parses the YAML configuration file.
func (l *YAMLLoader) Load() (*Config, error) {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	cfg, err := l.transformToConfig(&raw)
	if err != nil {
		return nil, fmt.Errorf("failed to transform config: %w", err)
	}

	return cfg, nil
}

// rawConfig represents the YAML structure as defined in the proposal.
type rawConfig struct {
	Name  string `yaml:"name"`
	Group string `yaml:"group"`

	IncomingAuth rawIncomingAuth `yaml:"incoming_auth"`
	OutgoingAuth rawOutgoingAuth `yaml:"outgoing_auth"`
	Aggregation  rawAggregation  `yaml:"aggregation"`
	TokenCache   *rawTokenCache  `yaml:"token_cache"`
	Operational  *rawOperational `yaml:"operational"`

	CompositeTools []*rawCompositeTool `yaml:"composite_tools"`
}

type rawIncomingAuth struct {
	Type string `yaml:"type"`
	OIDC *struct {
		Issuer          string   `yaml:"issuer"`
		ClientID        string   `yaml:"client_id"`
		ClientSecretEnv string   `yaml:"client_secret_env"` // Environment variable name containing the client secret
		Audience        string   `yaml:"audience"`
		Resource        string   `yaml:"resource"`
		Scopes          []string `yaml:"scopes"`
	} `yaml:"oidc"`
	Authz *struct {
		Type     string   `yaml:"type"`
		Policies []string `yaml:"policies"`
	} `yaml:"authz"`
}

type rawOutgoingAuth struct {
	Source   string                             `yaml:"source"`
	Default  *rawBackendAuthStrategy            `yaml:"default"`
	Backends map[string]*rawBackendAuthStrategy `yaml:"backends"`
}

type rawBackendAuthStrategy struct {
	Type            string                  `yaml:"type"`
	HeaderInjection *rawHeaderInjectionAuth `yaml:"header_injection"`
	TokenExchange   *rawTokenExchangeAuth   `yaml:"token_exchange"`
	ServiceAccount  *rawServiceAccountAuth  `yaml:"service_account"`
}

type rawHeaderInjectionAuth struct {
	HeaderName  string `yaml:"header_name"`
	HeaderValue string `yaml:"header_value"`
}

type rawTokenExchangeAuth struct {
	TokenURL         string   `yaml:"token_url"`
	ClientID         string   `yaml:"client_id"`
	ClientSecretEnv  string   `yaml:"client_secret_env"`
	Audience         string   `yaml:"audience"`
	Scopes           []string `yaml:"scopes"`
	SubjectTokenType string   `yaml:"subject_token_type"`
}

type rawServiceAccountAuth struct {
	CredentialsEnv string `yaml:"credentials_env"`
	HeaderName     string `yaml:"header_name"`
	HeaderFormat   string `yaml:"header_format"`
}

type rawAggregation struct {
	ConflictResolution       string                       `yaml:"conflict_resolution"`
	ConflictResolutionConfig *rawConflictResolutionConfig `yaml:"conflict_resolution_config"`
	Tools                    []*rawWorkloadToolConfig     `yaml:"tools"`
}

type rawConflictResolutionConfig struct {
	PrefixFormat  string   `yaml:"prefix_format"`
	PriorityOrder []string `yaml:"priority_order"`
}

type rawWorkloadToolConfig struct {
	Workload  string                      `yaml:"workload"`
	Filter    []string                    `yaml:"filter"`
	Overrides map[string]*rawToolOverride `yaml:"overrides"`
}

type rawToolOverride struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type rawTokenCache struct {
	Provider string `yaml:"provider"`
	Config   struct {
		MaxEntries int    `yaml:"max_entries"`
		TTLOffset  string `yaml:"ttl_offset"`
		Address    string `yaml:"address"`
		DB         int    `yaml:"db"`
		KeyPrefix  string `yaml:"key_prefix"`
		Password   string `yaml:"password"`
	} `yaml:"config"`
}

type rawOperational struct {
	Timeouts struct {
		Default     string            `yaml:"default"`
		PerWorkload map[string]string `yaml:"per_workload"`
	} `yaml:"timeouts"`
	FailureHandling struct {
		HealthCheckInterval string `yaml:"health_check_interval"`
		UnhealthyThreshold  int    `yaml:"unhealthy_threshold"`
		PartialFailureMode  string `yaml:"partial_failure_mode"`
		CircuitBreaker      struct {
			Enabled          bool   `yaml:"enabled"`
			FailureThreshold int    `yaml:"failure_threshold"`
			Timeout          string `yaml:"timeout"`
		} `yaml:"circuit_breaker"`
	} `yaml:"failure_handling"`
}

type rawCompositeTool struct {
	Name        string                    `yaml:"name"`
	Description string                    `yaml:"description"`
	Parameters  map[string]map[string]any `yaml:"parameters"`
	Timeout     string                    `yaml:"timeout"`
	Steps       []*rawWorkflowStep        `yaml:"steps"`
}

type rawWorkflowStep struct {
	ID        string                  `yaml:"id"`
	Type      string                  `yaml:"type"`
	Tool      string                  `yaml:"tool"`
	Arguments map[string]any          `yaml:"arguments"`
	Condition string                  `yaml:"condition"`
	DependsOn []string                `yaml:"depends_on"`
	OnError   *rawStepErrorHandling   `yaml:"on_error"`
	Message   string                  `yaml:"message"`
	Schema    map[string]any          `yaml:"schema"`
	Timeout   string                  `yaml:"timeout"`
	OnDecline *rawElicitationResponse `yaml:"on_decline"`
	OnCancel  *rawElicitationResponse `yaml:"on_cancel"`
}

type rawStepErrorHandling struct {
	Action     string `yaml:"action"`
	RetryCount int    `yaml:"retry_count"`
	RetryDelay string `yaml:"retry_delay"`
}

type rawElicitationResponse struct {
	Action string `yaml:"action"`
}

// transformToConfig converts the raw YAML structure to the unified Config model.
func (l *YAMLLoader) transformToConfig(raw *rawConfig) (*Config, error) {
	cfg := &Config{
		Name:  raw.Name,
		Group: raw.Group,
	}

	// Transform incoming auth
	incomingAuth, err := l.transformIncomingAuth(&raw.IncomingAuth)
	if err != nil {
		return nil, fmt.Errorf("incoming_auth: %w", err)
	}
	cfg.IncomingAuth = incomingAuth

	// Transform outgoing auth
	outgoingAuth, err := l.transformOutgoingAuth(&raw.OutgoingAuth)
	if err != nil {
		return nil, fmt.Errorf("outgoing_auth: %w", err)
	}
	cfg.OutgoingAuth = outgoingAuth

	// Transform aggregation
	aggregation, err := l.transformAggregation(&raw.Aggregation)
	if err != nil {
		return nil, fmt.Errorf("aggregation: %w", err)
	}
	cfg.Aggregation = aggregation

	// Transform token cache
	if raw.TokenCache != nil {
		tokenCache, err := l.transformTokenCache(raw.TokenCache)
		if err != nil {
			return nil, fmt.Errorf("token_cache: %w", err)
		}
		cfg.TokenCache = tokenCache
	}

	// Transform operational
	if raw.Operational != nil {
		operational, err := l.transformOperational(raw.Operational)
		if err != nil {
			return nil, fmt.Errorf("operational: %w", err)
		}
		cfg.Operational = operational
	}

	// Transform composite tools
	if len(raw.CompositeTools) > 0 {
		compositeTools, err := l.transformCompositeTools(raw.CompositeTools)
		if err != nil {
			return nil, fmt.Errorf("composite_tools: %w", err)
		}
		cfg.CompositeTools = compositeTools
	}

	return cfg, nil
}

//nolint:unparam // error return reserved for future validation logic
func (*YAMLLoader) transformIncomingAuth(raw *rawIncomingAuth) (*IncomingAuthConfig, error) {
	cfg := &IncomingAuthConfig{
		Type: raw.Type,
	}

	if raw.OIDC != nil {
		cfg.OIDC = &OIDCConfig{
			Issuer:          raw.OIDC.Issuer,
			ClientID:        raw.OIDC.ClientID,
			ClientSecretEnv: raw.OIDC.ClientSecretEnv,
			Audience:        raw.OIDC.Audience,
			Resource:        raw.OIDC.Resource,
			Scopes:          raw.OIDC.Scopes,
		}
	}

	if raw.Authz != nil {
		cfg.Authz = &AuthzConfig{
			Type:     raw.Authz.Type,
			Policies: raw.Authz.Policies,
		}
	}

	return cfg, nil
}

func (l *YAMLLoader) transformOutgoingAuth(raw *rawOutgoingAuth) (*OutgoingAuthConfig, error) {
	cfg := &OutgoingAuthConfig{
		Source:   raw.Source,
		Backends: make(map[string]*BackendAuthStrategy),
	}

	if raw.Default != nil {
		strategy, err := l.transformBackendAuthStrategy(raw.Default)
		if err != nil {
			return nil, fmt.Errorf("default: %w", err)
		}
		cfg.Default = strategy
	}

	for name, rawStrategy := range raw.Backends {
		strategy, err := l.transformBackendAuthStrategy(rawStrategy)
		if err != nil {
			return nil, fmt.Errorf("backend %s: %w", name, err)
		}
		cfg.Backends[name] = strategy
	}

	return cfg, nil
}

func (l *YAMLLoader) transformBackendAuthStrategy(raw *rawBackendAuthStrategy) (*BackendAuthStrategy, error) {
	strategy := &BackendAuthStrategy{
		Type:     raw.Type,
		Metadata: make(map[string]any),
	}

	switch raw.Type {
	case strategies.StrategyTypeHeaderInjection:
		if raw.HeaderInjection == nil {
			return nil, fmt.Errorf("header_injection configuration is required")
		}

		strategy.Metadata = map[string]any{
			strategies.MetadataHeaderName:  raw.HeaderInjection.HeaderName,
			strategies.MetadataHeaderValue: raw.HeaderInjection.HeaderValue,
		}

	case strategies.StrategyTypeUnauthenticated:
		// No metadata required for unauthenticated strategy

	case "token_exchange":
		if raw.TokenExchange == nil {
			return nil, fmt.Errorf("token_exchange configuration is required")
		}

		// Validate that environment variable is set (but don't resolve it yet)
		if raw.TokenExchange.ClientSecretEnv != "" {
			if l.envReader.Getenv(raw.TokenExchange.ClientSecretEnv) == "" {
				return nil, fmt.Errorf("environment variable %s not set", raw.TokenExchange.ClientSecretEnv)
			}
		}

		strategy.Metadata = map[string]any{
			"token_url":          raw.TokenExchange.TokenURL,
			"client_id":          raw.TokenExchange.ClientID,
			"client_secret_env":  raw.TokenExchange.ClientSecretEnv,
			"audience":           raw.TokenExchange.Audience,
			"scopes":             raw.TokenExchange.Scopes,
			"subject_token_type": raw.TokenExchange.SubjectTokenType,
		}

	case "service_account":
		if raw.ServiceAccount == nil {
			return nil, fmt.Errorf("service_account configuration is required")
		}

		// Resolve credentials from environment
		credentials := l.envReader.Getenv(raw.ServiceAccount.CredentialsEnv)
		if credentials == "" {
			return nil, fmt.Errorf("environment variable %s not set", raw.ServiceAccount.CredentialsEnv)
		}

		strategy.Metadata = map[string]any{
			"credentials":     credentials,
			"credentials_env": raw.ServiceAccount.CredentialsEnv,
			"header_name":     raw.ServiceAccount.HeaderName,
			"header_format":   raw.ServiceAccount.HeaderFormat,
		}
	}

	return strategy, nil
}

// transformAggregation transforms raw aggregation configuration.
// Error return is maintained for consistency with other transform methods and future validation.
//
//nolint:unparam // error return kept for interface consistency
func (*YAMLLoader) transformAggregation(raw *rawAggregation) (*AggregationConfig, error) {
	strategy := vmcp.ConflictResolutionStrategy(raw.ConflictResolution)

	cfg := &AggregationConfig{
		ConflictResolution:       strategy,
		ConflictResolutionConfig: &ConflictResolutionConfig{},
	}

	if raw.ConflictResolutionConfig != nil {
		cfg.ConflictResolutionConfig.PrefixFormat = raw.ConflictResolutionConfig.PrefixFormat
		cfg.ConflictResolutionConfig.PriorityOrder = raw.ConflictResolutionConfig.PriorityOrder
	}

	for _, rawTool := range raw.Tools {
		tool := &WorkloadToolConfig{
			Workload:  rawTool.Workload,
			Filter:    rawTool.Filter,
			Overrides: make(map[string]*ToolOverride),
		}

		for name, override := range rawTool.Overrides {
			tool.Overrides[name] = &ToolOverride{
				Name:        override.Name,
				Description: override.Description,
			}
		}

		cfg.Tools = append(cfg.Tools, tool)
	}

	return cfg, nil
}

func (*YAMLLoader) transformTokenCache(raw *rawTokenCache) (*TokenCacheConfig, error) {
	cfg := &TokenCacheConfig{
		Provider: raw.Provider,
	}

	switch raw.Provider {
	case CacheProviderMemory:
		ttlOffset, err := time.ParseDuration(raw.Config.TTLOffset)
		if err != nil {
			return nil, fmt.Errorf("invalid ttl_offset: %w", err)
		}

		cfg.Memory = &MemoryCacheConfig{
			MaxEntries: raw.Config.MaxEntries,
			TTLOffset:  Duration(ttlOffset),
		}

	case CacheProviderRedis:
		ttlOffset, err := time.ParseDuration(raw.Config.TTLOffset)
		if err != nil {
			return nil, fmt.Errorf("invalid ttl_offset: %w", err)
		}

		cfg.Redis = &RedisCacheConfig{
			Address:   raw.Config.Address,
			DB:        raw.Config.DB,
			KeyPrefix: raw.Config.KeyPrefix,
			Password:  raw.Config.Password,
			TTLOffset: Duration(ttlOffset),
		}
	}

	return cfg, nil
}

func (*YAMLLoader) transformOperational(raw *rawOperational) (*OperationalConfig, error) {
	cfg := &OperationalConfig{}

	// Transform timeouts
	if raw.Timeouts.Default != "" {
		defaultTimeout, err := time.ParseDuration(raw.Timeouts.Default)
		if err != nil {
			return nil, fmt.Errorf("invalid default timeout: %w", err)
		}

		cfg.Timeouts = &TimeoutConfig{
			Default:     Duration(defaultTimeout),
			PerWorkload: make(map[string]Duration),
		}

		for workload, timeoutStr := range raw.Timeouts.PerWorkload {
			timeout, err := time.ParseDuration(timeoutStr)
			if err != nil {
				return nil, fmt.Errorf("invalid timeout for workload %s: %w", workload, err)
			}
			cfg.Timeouts.PerWorkload[workload] = Duration(timeout)
		}
	}

	// Transform failure handling
	healthCheckInterval, err := time.ParseDuration(raw.FailureHandling.HealthCheckInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid health_check_interval: %w", err)
	}

	cfg.FailureHandling = &FailureHandlingConfig{
		HealthCheckInterval: Duration(healthCheckInterval),
		UnhealthyThreshold:  raw.FailureHandling.UnhealthyThreshold,
		PartialFailureMode:  raw.FailureHandling.PartialFailureMode,
	}

	// Transform circuit breaker
	if raw.FailureHandling.CircuitBreaker.Enabled {
		cbTimeout, err := time.ParseDuration(raw.FailureHandling.CircuitBreaker.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid circuit_breaker timeout: %w", err)
		}

		cfg.FailureHandling.CircuitBreaker = &CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: raw.FailureHandling.CircuitBreaker.FailureThreshold,
			Timeout:          Duration(cbTimeout),
		}
	}

	return cfg, nil
}

func (l *YAMLLoader) transformCompositeTools(raw []*rawCompositeTool) ([]*CompositeToolConfig, error) {
	var tools []*CompositeToolConfig

	for _, rawTool := range raw {
		timeout, err := time.ParseDuration(rawTool.Timeout)
		if err != nil {
			return nil, fmt.Errorf("tool %s: invalid timeout: %w", rawTool.Name, err)
		}

		tool := &CompositeToolConfig{
			Name:        rawTool.Name,
			Description: rawTool.Description,
			Parameters:  make(map[string]ParameterSchema),
			Timeout:     Duration(timeout),
		}

		// Transform parameters
		for name, paramMap := range rawTool.Parameters {
			typeVal, ok := paramMap["type"]
			if !ok {
				return nil, fmt.Errorf("tool %s, parameter %s: missing 'type' field", rawTool.Name, name)
			}
			typeStr, ok := typeVal.(string)
			if !ok {
				return nil, fmt.Errorf("tool %s, parameter %s: 'type' field must be a string", rawTool.Name, name)
			}
			param := ParameterSchema{
				Type: typeStr,
			}
			if def, ok := paramMap["default"]; ok {
				param.Default = def
			}
			tool.Parameters[name] = param
		}

		// Transform steps
		for _, rawStep := range rawTool.Steps {
			step, err := l.transformWorkflowStep(rawStep)
			if err != nil {
				return nil, fmt.Errorf("tool %s, step %s: %w", rawTool.Name, rawStep.ID, err)
			}
			tool.Steps = append(tool.Steps, step)
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

func (*YAMLLoader) transformWorkflowStep(raw *rawWorkflowStep) (*WorkflowStepConfig, error) {
	step := &WorkflowStepConfig{
		ID:        raw.ID,
		Type:      raw.Type,
		Tool:      raw.Tool,
		Arguments: raw.Arguments,
		Condition: raw.Condition,
		DependsOn: raw.DependsOn,
		Message:   raw.Message,
		Schema:    raw.Schema,
	}

	if raw.Timeout != "" {
		timeout, err := time.ParseDuration(raw.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout: %w", err)
		}
		step.Timeout = Duration(timeout)
	} else if raw.Type == "elicitation" {
		// Set default timeout for elicitation steps
		step.Timeout = Duration(5 * time.Minute)
	}

	if raw.OnError != nil {
		step.OnError = &StepErrorHandling{
			Action:     raw.OnError.Action,
			RetryCount: raw.OnError.RetryCount,
		}
		if raw.OnError.RetryDelay != "" {
			retryDelay, err := time.ParseDuration(raw.OnError.RetryDelay)
			if err != nil {
				return nil, fmt.Errorf("invalid retry_delay: %w", err)
			}
			step.OnError.RetryDelay = Duration(retryDelay)
		}
	}

	if raw.OnDecline != nil {
		step.OnDecline = &ElicitationResponseConfig{
			Action: raw.OnDecline.Action,
		}
	}

	if raw.OnCancel != nil {
		step.OnCancel = &ElicitationResponseConfig{
			Action: raw.OnCancel.Action,
		}
	}

	return step, nil
}

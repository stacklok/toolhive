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
	Operational  *rawOperational `yaml:"operational"`

	CompositeTools []*rawCompositeTool `yaml:"composite_tools"`
}

type rawIncomingAuth struct {
	Type string `yaml:"type"`
	OIDC *struct {
		Issuer                          string   `yaml:"issuer"`
		ClientID                        string   `yaml:"client_id"`
		ClientSecretEnv                 string   `yaml:"client_secret_env"` // Environment variable name containing the client secret
		Audience                        string   `yaml:"audience"`
		Resource                        string   `yaml:"resource"`
		Scopes                          []string `yaml:"scopes"`
		ProtectedResourceAllowPrivateIP bool     `yaml:"protected_resource_allow_private_ip"`
		InsecureAllowHTTP               bool     `yaml:"insecure_allow_http"`
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
}

type rawHeaderInjectionAuth struct {
	HeaderName     string `yaml:"header_name"`
	HeaderValue    string `yaml:"header_value"`
	HeaderValueEnv string `yaml:"header_value_env"`
}

type rawTokenExchangeAuth struct {
	TokenURL         string   `yaml:"token_url"`
	ClientID         string   `yaml:"client_id"`
	ClientSecretEnv  string   `yaml:"client_secret_env"`
	Audience         string   `yaml:"audience"`
	Scopes           []string `yaml:"scopes"`
	SubjectTokenType string   `yaml:"subject_token_type"`
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
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	Parameters  map[string]any     `yaml:"parameters"` // Full JSON Schema format
	Timeout     string             `yaml:"timeout"`
	Steps       []*rawWorkflowStep `yaml:"steps"`
	Output      *rawOutputConfig   `yaml:"output"`
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

type rawOutputConfig struct {
	Properties map[string]rawOutputProperty `yaml:"properties"`
	Required   []string                     `yaml:"required"`
}

type rawOutputProperty struct {
	Type        string                       `yaml:"type"`
	Description string                       `yaml:"description"`
	Value       string                       `yaml:"value"`
	Properties  map[string]rawOutputProperty `yaml:"properties"`
	Default     any                          `yaml:"default"`
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

	// Apply operational defaults (fills missing values)
	cfg.EnsureOperationalDefaults()

	return cfg, nil
}

//nolint:unparam // error return reserved for future validation logic
func (*YAMLLoader) transformIncomingAuth(raw *rawIncomingAuth) (*IncomingAuthConfig, error) {
	cfg := &IncomingAuthConfig{
		Type: raw.Type,
	}

	if raw.OIDC != nil {
		cfg.OIDC = &OIDCConfig{
			Issuer:                          raw.OIDC.Issuer,
			ClientID:                        raw.OIDC.ClientID,
			ClientSecretEnv:                 raw.OIDC.ClientSecretEnv,
			Audience:                        raw.OIDC.Audience,
			Resource:                        raw.OIDC.Resource,
			Scopes:                          raw.OIDC.Scopes,
			ProtectedResourceAllowPrivateIP: raw.OIDC.ProtectedResourceAllowPrivateIP,
			InsecureAllowHTTP:               raw.OIDC.InsecureAllowHTTP,
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

//nolint:gocyclo // We should split this into multiple functions per strategy type.
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

		// Validate that exactly one of header_value or header_value_env is set
		// to make the life of the strategy easier, we read the value here in set preference
		// order and pass it in metadata in a single value regardless of how it was set.
		hasValue := raw.HeaderInjection.HeaderValue != ""
		hasValueEnv := raw.HeaderInjection.HeaderValueEnv != ""

		if hasValue && hasValueEnv {
			return nil, fmt.Errorf("header_injection: only one of header_value or header_value_env must be set")
		}
		if !hasValue && !hasValueEnv {
			return nil, fmt.Errorf("header_injection: either header_value or header_value_env must be set")
		}

		// Resolve header value from environment if env var name is provided
		headerValue := raw.HeaderInjection.HeaderValue
		if hasValueEnv {
			headerValue = l.envReader.Getenv(raw.HeaderInjection.HeaderValueEnv)
			if headerValue == "" {
				return nil, fmt.Errorf("environment variable %s not set or empty", raw.HeaderInjection.HeaderValueEnv)
			}
		}

		strategy.Metadata = map[string]any{
			strategies.MetadataHeaderName:  raw.HeaderInjection.HeaderName,
			strategies.MetadataHeaderValue: headerValue,
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
		// Parse timeout - empty string means use default (0 duration)
		var timeout time.Duration
		if rawTool.Timeout != "" {
			var err error
			timeout, err = time.ParseDuration(rawTool.Timeout)
			if err != nil {
				return nil, fmt.Errorf("tool %s: invalid timeout: %w", rawTool.Name, err)
			}
		}

		tool := &CompositeToolConfig{
			Name:        rawTool.Name,
			Description: rawTool.Description,
			Parameters:  rawTool.Parameters, // Pass through JSON Schema directly
			Timeout:     Duration(timeout),
		}

		// Validate parameters is valid JSON Schema if present
		if len(rawTool.Parameters) > 0 {
			if err := validateParametersJSONSchema(rawTool.Parameters, rawTool.Name); err != nil {
				return nil, err
			}
		}

		// Transform steps
		for _, rawStep := range rawTool.Steps {
			step, err := l.transformWorkflowStep(rawStep)
			if err != nil {
				return nil, fmt.Errorf("tool %s, step %s: %w", rawTool.Name, rawStep.ID, err)
			}
			tool.Steps = append(tool.Steps, step)
		}

		// Transform output config
		if rawTool.Output != nil {
			outputCfg, err := l.transformOutputConfig(rawTool.Output)
			if err != nil {
				return nil, fmt.Errorf("tool %s, output: %w", rawTool.Name, err)
			}
			tool.Output = outputCfg
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

	// Apply type inference: if type is empty and tool field is present, infer as "tool"
	if step.Type == "" && step.Tool != "" {
		step.Type = "tool"
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

func (*YAMLLoader) transformOutputConfig(raw *rawOutputConfig) (*OutputConfig, error) {
	if raw == nil {
		return nil, nil
	}

	cfg := &OutputConfig{
		Properties: make(map[string]OutputProperty),
		Required:   raw.Required,
	}

	for name, rawProp := range raw.Properties {
		prop, err := transformOutputProperty(&rawProp)
		if err != nil {
			return nil, fmt.Errorf("property %s: %w", name, err)
		}
		cfg.Properties[name] = prop
	}

	return cfg, nil
}

func transformOutputProperty(raw *rawOutputProperty) (OutputProperty, error) {
	prop := OutputProperty{
		Type:        raw.Type,
		Description: raw.Description,
		Value:       raw.Value,
		Default:     raw.Default,
	}

	// Transform nested properties for object types
	if len(raw.Properties) > 0 {
		prop.Properties = make(map[string]OutputProperty)
		for name, rawNestedProp := range raw.Properties {
			nestedProp, err := transformOutputProperty(&rawNestedProp)
			if err != nil {
				return OutputProperty{}, fmt.Errorf("nested property %s: %w", name, err)
			}
			prop.Properties[name] = nestedProp
		}
	}

	return prop, nil
}

// validateParametersJSONSchema validates that parameters follows JSON Schema format.
// Per MCP specification, parameters should be a JSON Schema object with type "object".
//
// We enforce type="object" because MCP tools use named parameters (inputSchema.properties),
// and non-object types (e.g., type="string") would mean a tool takes a single unnamed value,
// which doesn't align with how MCP tool arguments work. The MCP SDK and specification
// expect tools to have named parameters accessible via inputSchema.properties.
func validateParametersJSONSchema(params map[string]any, toolName string) error {
	if len(params) == 0 {
		return nil
	}

	// Check if it has "type" field
	typeVal, hasType := params["type"]
	if !hasType {
		return fmt.Errorf("tool %s: parameters must have 'type' field (should be 'object' for JSON Schema)", toolName)
	}

	// Type must be a string
	typeStr, ok := typeVal.(string)
	if !ok {
		return fmt.Errorf("tool %s: parameters 'type' field must be a string", toolName)
	}

	// Type should be "object" for parameter schemas
	if typeStr != "object" {
		return fmt.Errorf("tool %s: parameters 'type' must be 'object' (got '%s')", toolName, typeStr)
	}

	// If properties exist, validate it's a map
	if properties, hasProps := params["properties"]; hasProps {
		if _, ok := properties.(map[string]any); !ok {
			return fmt.Errorf("tool %s: parameters 'properties' must be an object", toolName)
		}
	}

	// If required exists, validate it's an array
	if required, hasRequired := params["required"]; hasRequired {
		if _, ok := required.([]any); !ok {
			// Also accept []string which may come from YAML
			if _, ok := required.([]string); !ok {
				return fmt.Errorf("tool %s: parameters 'required' must be an array", toolName)
			}
		}
	}

	return nil
}

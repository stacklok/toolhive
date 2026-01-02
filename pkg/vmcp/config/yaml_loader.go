package config

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/env"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
// Uses strict unmarshalling to reject unknown fields.
func (l *YAMLLoader) Load() (*Config, error) {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Use yaml.Decoder with KnownFields for strict unmarshalling
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true) // Reject unknown fields

	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Post-process the config
	if err := l.postProcess(&cfg); err != nil {
		return nil, fmt.Errorf("failed to process config: %w", err)
	}

	return &cfg, nil
}

// postProcess applies post-load processing to the config:
// - Resolves environment variables for secrets
// - Applies type inference for workflow steps
// - Sets default timeouts
// - Validates JSON schemas
func (l *YAMLLoader) postProcess(cfg *Config) error {
	// Process outgoing auth - resolve env vars
	if cfg.OutgoingAuth != nil {
		if err := l.processOutgoingAuth(cfg.OutgoingAuth); err != nil {
			return fmt.Errorf("outgoing_auth: %w", err)
		}
	}

	// Process composite tools - type inference, defaults, validation
	for i, tool := range cfg.CompositeTools {
		if err := l.processCompositeTool(tool); err != nil {
			return fmt.Errorf("composite_tools[%d]: %w", i, err)
		}
	}

	// Apply operational defaults (fills missing values)
	cfg.EnsureOperationalDefaults()

	return nil
}

// processOutgoingAuth resolves environment variables for auth strategies.
func (l *YAMLLoader) processOutgoingAuth(auth *OutgoingAuthConfig) error {
	if auth.Default != nil {
		if err := l.processBackendAuthStrategy("default", auth.Default); err != nil {
			return err
		}
	}

	for name, strategy := range auth.Backends {
		if err := l.processBackendAuthStrategy(name, strategy); err != nil {
			return err
		}
	}

	return nil
}

// processBackendAuthStrategy resolves environment variables for a single auth strategy.
//
//nolint:gocyclo // Strategy-specific processing requires checking multiple fields
func (l *YAMLLoader) processBackendAuthStrategy(name string, strategy *authtypes.BackendAuthStrategy) error {
	switch strategy.Type {
	case authtypes.StrategyTypeHeaderInjection:
		if strategy.HeaderInjection == nil {
			return fmt.Errorf("backend %s: header_injection configuration is required", name)
		}

		hi := strategy.HeaderInjection
		hasValue := hi.HeaderValue != ""
		hasValueEnv := hi.HeaderValueEnv != ""

		if hasValue && hasValueEnv {
			return fmt.Errorf("backend %s: only one of header_value or header_value_env must be set", name)
		}
		if !hasValue && !hasValueEnv {
			return fmt.Errorf("backend %s: either header_value or header_value_env must be set", name)
		}

		// Resolve header value from environment if env var name is provided
		if hasValueEnv {
			hi.HeaderValue = l.envReader.Getenv(hi.HeaderValueEnv)
			if hi.HeaderValue == "" {
				return fmt.Errorf("backend %s: environment variable %s not set or empty", name, hi.HeaderValueEnv)
			}
		}

	case authtypes.StrategyTypeTokenExchange:
		if strategy.TokenExchange == nil {
			return fmt.Errorf("backend %s: token_exchange configuration is required", name)
		}

		te := strategy.TokenExchange
		if te.ClientSecretEnv != "" {
			// Validate that the environment variable is set
			resolvedSecret := l.envReader.Getenv(te.ClientSecretEnv)
			if resolvedSecret == "" {
				return fmt.Errorf("backend %s: environment variable %s not set", name, te.ClientSecretEnv)
			}
		}

	case authtypes.StrategyTypeUnauthenticated:
		// No validation needed

	default:
		// Unknown strategy type - let validation handle it
	}

	return nil
}

// processCompositeTool applies post-processing to a composite tool.
func (l *YAMLLoader) processCompositeTool(tool *CompositeToolConfig) error {
	// Validate parameters JSON Schema if present
	if len(tool.Parameters) > 0 {
		if err := validateParametersJSONSchema(tool.Parameters, tool.Name); err != nil {
			return err
		}
	}

	// Process each step
	for _, step := range tool.Steps {
		l.processWorkflowStep(step)
	}

	return nil
}

// processWorkflowStep applies post-processing to a workflow step.
func (*YAMLLoader) processWorkflowStep(step *WorkflowStepConfig) {
	// Apply type inference: if type is empty and tool field is present, infer as "tool"
	if step.Type == "" && step.Tool != "" {
		step.Type = "tool"
	}

	// Set default timeout for elicitation steps
	if step.Type == "elicitation" && step.Timeout == 0 {
		step.Timeout = Duration(5 * time.Minute)
	}
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

package resolver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/env"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// Ensure CLIAuthResolver implements AuthResolver
var _ AuthResolver = (*CLIAuthResolver)(nil)

// CLIAuthResolver implements AuthResolver for CLI environments.
// It resolves external auth config references by loading YAML files from a
// configuration directory and resolving secrets from environment variables.
//
// This provides feature parity with K8SAuthResolver but uses local files
// and environment variables instead of Kubernetes resources.
type CLIAuthResolver struct {
	envReader env.Reader
	configDir string
}

// NewCLIAuthResolver creates a new CLI-based auth resolver.
//
// Parameters:
//   - envReader: Reader for accessing environment variables (use &env.OSReader{} for production)
//   - configDir: Directory containing external auth config YAML files (e.g., ~/.toolhive/vmcp/auth-configs/)
func NewCLIAuthResolver(envReader env.Reader, configDir string) *CLIAuthResolver {
	return &CLIAuthResolver{
		envReader: envReader,
		configDir: configDir,
	}
}

// ResolveExternalAuthConfig resolves an external auth config reference name
// to a concrete BackendAuthStrategy by loading a YAML file from the config
// directory and resolving environment variable references.
//
// The method looks for a file named {refName}.yaml in the config directory,
// parses it as an ExternalAuthConfig, and converts it to a BackendAuthStrategy
// with all secrets resolved from environment variables.
func (r *CLIAuthResolver) ResolveExternalAuthConfig(
	_ context.Context,
	refName string,
) (*authtypes.BackendAuthStrategy, error) {
	if refName == "" {
		return nil, fmt.Errorf("external auth config ref name is empty")
	}

	// Build the path to the config file
	configPath := filepath.Join(r.configDir, refName+".yaml")

	// Load and parse the config file
	config, err := r.loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load external auth config %q: %w", refName, err)
	}

	// Convert to BackendAuthStrategy with resolved secrets
	strategy, err := r.convertToStrategy(config)
	if err != nil {
		return nil, fmt.Errorf("failed to convert external auth config %q: %w", refName, err)
	}

	return strategy, nil
}

// loadConfig loads and parses an ExternalAuthConfig from a YAML file.
func (*CLIAuthResolver) loadConfig(configPath string) (*ExternalAuthConfig, error) {
	data, err := os.ReadFile(configPath) //nolint:gosec // configPath is constructed from configDir + refName, both controlled
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found: %s", configPath)
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config ExternalAuthConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	return &config, nil
}

// convertToStrategy converts an ExternalAuthConfig to a BackendAuthStrategy,
// resolving all environment variable references to actual values.
func (r *CLIAuthResolver) convertToStrategy(config *ExternalAuthConfig) (*authtypes.BackendAuthStrategy, error) {
	switch config.Type {
	case "token_exchange":
		return r.convertTokenExchange(config.TokenExchange)
	case "header_injection":
		return r.convertHeaderInjection(config.HeaderInjection)
	default:
		return nil, fmt.Errorf("unknown auth type %q (expected 'token_exchange' or 'header_injection')", config.Type)
	}
}

// convertTokenExchange converts a TokenExchangeYAMLConfig to a BackendAuthStrategy.
func (r *CLIAuthResolver) convertTokenExchange(config *TokenExchangeYAMLConfig) (*authtypes.BackendAuthStrategy, error) {
	if config == nil {
		return nil, fmt.Errorf("token_exchange config is nil but type is 'token_exchange'")
	}

	// Validate required fields
	if config.TokenURL == "" {
		return nil, fmt.Errorf("token_url is required for token_exchange")
	}
	if config.ClientSecretEnv == "" {
		return nil, fmt.Errorf("client_secret_env is required for token_exchange")
	}

	// Resolve the client secret from environment variable
	clientSecret := r.envReader.Getenv(config.ClientSecretEnv)
	if clientSecret == "" {
		return nil, fmt.Errorf("environment variable %q is not set or empty (required for client_secret)", config.ClientSecretEnv)
	}

	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeTokenExchange,
		TokenExchange: &authtypes.TokenExchangeConfig{
			TokenURL:     config.TokenURL,
			ClientID:     config.ClientID,
			ClientSecret: clientSecret,
			Audience:     config.Audience,
			Scopes:       config.Scopes,
		},
	}, nil
}

// convertHeaderInjection converts a HeaderInjectionYAMLConfig to a BackendAuthStrategy.
func (r *CLIAuthResolver) convertHeaderInjection(config *HeaderInjectionYAMLConfig) (*authtypes.BackendAuthStrategy, error) {
	if config == nil {
		return nil, fmt.Errorf("header_injection config is nil but type is 'header_injection'")
	}

	// Validate required fields
	if config.HeaderName == "" {
		return nil, fmt.Errorf("header_name is required for header_injection")
	}
	if config.HeaderValueEnv == "" {
		return nil, fmt.Errorf("header_value_env is required for header_injection")
	}

	// Resolve the header value from environment variable
	headerValue := r.envReader.Getenv(config.HeaderValueEnv)
	if headerValue == "" {
		return nil, fmt.Errorf("environment variable %q is not set or empty (required for header_value)", config.HeaderValueEnv)
	}

	return &authtypes.BackendAuthStrategy{
		Type: authtypes.StrategyTypeHeaderInjection,
		HeaderInjection: &authtypes.HeaderInjectionConfig{
			HeaderName:  config.HeaderName,
			HeaderValue: headerValue,
		},
	}, nil
}

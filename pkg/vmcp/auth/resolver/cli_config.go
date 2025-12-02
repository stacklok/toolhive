package resolver

// ExternalAuthConfig represents the YAML configuration for an external auth config
// used by CLI mode. This mirrors the MCPExternalAuthConfig CRD structure but uses
// environment variable references for secrets instead of Kubernetes Secret references.
//
// Example YAML file (~/.toolhive/vmcp/auth-configs/github-oauth.yaml):
//
//	type: token_exchange
//	token_exchange:
//	  token_url: https://github.com/login/oauth/access_token
//	  client_id: my-client-id
//	  client_secret_env: GITHUB_CLIENT_SECRET
//	  audience: https://api.github.com
//	  scopes:
//	    - repo
type ExternalAuthConfig struct {
	// Type specifies the authentication type.
	// Valid values: "token_exchange", "header_injection"
	Type string `yaml:"type"`

	// TokenExchange contains configuration for OAuth token exchange.
	// Required when Type is "token_exchange".
	TokenExchange *TokenExchangeYAMLConfig `yaml:"token_exchange,omitempty"`

	// HeaderInjection contains configuration for static header injection.
	// Required when Type is "header_injection".
	HeaderInjection *HeaderInjectionYAMLConfig `yaml:"header_injection,omitempty"`
}

// TokenExchangeYAMLConfig contains the configuration for OAuth token exchange
// in CLI mode. Secrets are resolved from environment variables.
type TokenExchangeYAMLConfig struct {
	// TokenURL is the OAuth token endpoint URL.
	TokenURL string `yaml:"token_url"`

	// ClientID is the OAuth client ID.
	ClientID string `yaml:"client_id"`

	// ClientSecretEnv is the name of the environment variable containing the client secret.
	// The actual secret value will be read from this environment variable at runtime.
	ClientSecretEnv string `yaml:"client_secret_env"`

	// Audience is the intended audience for the token (optional).
	Audience string `yaml:"audience,omitempty"`

	// Scopes are the OAuth scopes to request (optional).
	Scopes []string `yaml:"scopes,omitempty"`
}

// HeaderInjectionYAMLConfig contains the configuration for static header injection
// in CLI mode. The header value is resolved from an environment variable.
type HeaderInjectionYAMLConfig struct {
	// HeaderName is the name of the header to inject.
	HeaderName string `yaml:"header_name"`

	// HeaderValueEnv is the name of the environment variable containing the header value.
	// The actual value will be read from this environment variable at runtime.
	HeaderValueEnv string `yaml:"header_value_env"`
}

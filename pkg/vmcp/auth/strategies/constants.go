// Package strategies provides authentication strategy implementations for Virtual MCP Server.
package strategies

// Strategy type identifiers used to identify authentication strategies.
const (
	// StrategyTypeUnauthenticated identifies the unauthenticated strategy.
	// This strategy performs no authentication and is used when a backend
	// requires no authentication.
	StrategyTypeUnauthenticated = "unauthenticated"

	// StrategyTypeHeaderInjection identifies the header injection strategy.
	// This strategy injects a static header value into request headers.
	StrategyTypeHeaderInjection = "header_injection"

	// StrategyTypeTokenExchange identifies the token exchange strategy.
	// This strategy exchanges an incoming token for a new token to use
	// when authenticating to the backend service.
	StrategyTypeTokenExchange = "token_exchange"
)

// Metadata key names used in strategy configurations.
const (
	// MetadataHeaderName is the key for the HTTP header name in metadata.
	// Used by HeaderInjectionStrategy to identify which header to inject.
	MetadataHeaderName = "header_name"

	// MetadataHeaderValue is the key for the HTTP header value in metadata.
	// Used by HeaderInjectionStrategy to identify the value to inject.
	MetadataHeaderValue = "header_value"

	// MetadataHeaderValueEnv is the key for the environment variable name
	// that contains the header value. Used by converters during the conversion
	// phase to indicate that secret resolution is needed. This is replaced with
	// MetadataHeaderValue (containing the actual secret) before reaching the strategy.
	MetadataHeaderValueEnv = "header_value_env"
)

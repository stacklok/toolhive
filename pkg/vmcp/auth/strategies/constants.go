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
)

// Metadata key names used in strategy configurations.
const (
	// MetadataHeaderName is the key for the HTTP header name in metadata.
	// Used by HeaderInjectionStrategy to identify which header to inject.
	MetadataHeaderName = "header_name"

	// MetadataHeaderValue is the key for the HTTP header value in metadata.
	// Used by HeaderInjectionStrategy to identify the value to inject.
	MetadataHeaderValue = "header_value"
)

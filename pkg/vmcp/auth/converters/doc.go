// Package converters provides Kubernetes-specific conversion of external
// authentication configurations to vMCP BackendAuthStrategy structures.
//
// # Package Responsibilities
//
// This package handles:
//   - Converting MCPExternalAuthConfig CRDs to BackendAuthStrategy structs
//   - Resolving Kubernetes Secret references to actual values
//   - Providing type-specific converters (TokenExchange, HeaderInjection)
//   - Managing a registry of StrategyConverter implementations
//
// # Relationship with resolver Package
//
// The converters package is a low-level Kubernetes-specific package that
// works directly with MCPExternalAuthConfig CRD objects. It is used by:
//
//  1. MCPServer controller: When building env vars for pods with known auth configs
//  2. K8SAuthResolver: When resolving external_auth_config_ref at runtime
//
// The resolver package (pkg/vmcp/auth/resolver) provides a higher-level,
// platform-agnostic interface (AuthResolver) that allows the BackendDiscoverer
// to resolve auth references without knowing whether the underlying
// implementation uses Kubernetes CRDs or local YAML files.
//
// # Architecture
//
//	BackendDiscoverer (aggregator)
//	        |
//	        | uses AuthResolver interface
//	        v
//	+-----------------------------------------------+
//	|            resolver.AuthResolver              |
//	|         (platform-agnostic interface)         |
//	+---------------+-------------------------------+
//	                |
//	    +-----------+-----------+
//	    v                       v
//	K8SAuthResolver      CLIAuthResolver
//	    |                (YAML + env vars)
//	    | delegates to
//	    v
//	converters.DiscoverAndResolveAuth
//	(K8s CRDs + Secrets)
//
// # Core Types
//
//   - StrategyConverter: Interface for type-specific converters (ConvertToStrategy, ResolveSecrets)
//   - Registry: Thread-safe registry of StrategyConverter implementations
//   - TokenExchangeConverter: Handles OAuth token exchange configurations
//   - HeaderInjectionConverter: Handles static header injection configurations
//
// # Entry Points
//
//   - DiscoverAndResolveAuth: Main entry point for discovering auth from MCPServer refs
//   - ConvertToStrategy: Convert an MCPExternalAuthConfig to BackendAuthStrategy
//   - ResolveSecretsForStrategy: Resolve Kubernetes secrets for a given strategy
//   - DefaultRegistry: Get the singleton registry with all built-in converters
//
// # Usage Context
//
// Use converters package when:
//   - Working directly with MCPExternalAuthConfig CRD objects
//   - Building pod environment variables in the MCPServer controller
//   - Implementing K8s-specific auth discovery
//
// Use resolver package when:
//   - You need platform-agnostic auth reference resolution
//   - Working in the aggregator/discoverer layer
//   - Supporting both K8s and CLI environments
package converters

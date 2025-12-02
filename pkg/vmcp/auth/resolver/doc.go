// Package resolver provides platform-agnostic authentication resolution for
// the vMCP aggregator subsystem.
//
// # Package Responsibilities
//
// This package provides:
//   - AuthResolver interface for resolving external_auth_config_ref strategy types
//   - K8SAuthResolver: Kubernetes implementation (delegates to converters package)
//   - CLIAuthResolver: CLI implementation (loads YAML files, resolves env vars)
//
// # Relationship with converters Package
//
// The resolver package provides a platform-agnostic abstraction layer on top
// of environment-specific implementations:
//
//   - K8SAuthResolver delegates to converters.DiscoverAndResolveAuth() internally,
//     reusing the existing CRD lookup and secret resolution logic.
//
//   - CLIAuthResolver implements its own logic to provide feature parity in CLI
//     environments, loading YAML files from ~/.toolhive/vmcp/auth-configs/
//     and resolving secrets from environment variables.
//
// This separation allows the BackendDiscoverer to work identically in both
// environments without conditional platform-specific logic.
//
// # When to Use Each Package
//
//	Use converters when:           Use resolver when:
//	- Working with K8s CRDs        - Need platform-agnostic auth resolution
//	- In MCPServer controller      - In aggregator/discoverer layer
//	- Building pod env vars        - Supporting both K8s and CLI modes
//	- Need K8s Secret access       - Resolving external_auth_config_ref at runtime
//
// # Configuration (CLI mode)
//
// For CLI mode, external auth configs are stored as YAML files:
//
//	~/.toolhive/vmcp/auth-configs/{name}.yaml
//
// The directory can be overridden by passing a custom configDir to NewCLIAuthResolver.
//
// # Example YAML Config (CLI mode)
//
//	# ~/.toolhive/vmcp/auth-configs/github-oauth.yaml
//	type: token_exchange
//	token_exchange:
//	  token_url: https://github.com/login/oauth/access_token
//	  client_id: my-client-id
//	  client_secret_env: GITHUB_CLIENT_SECRET
//	  audience: https://api.github.com
//	  scopes:
//	    - repo
package resolver

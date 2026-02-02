# Auth Server Integration Tests

This directory contains integration tests for the embedded authorization server that integrates with the proxy runner.

## Overview

These tests verify that the auth server:
- Correctly integrates with the proxy runner
- Serves OAuth/OIDC endpoints at the correct paths
- Operates alongside MCP endpoints without conflict
- Handles configuration validation properly
- Cleans up resources correctly

## Directory Structure

```
test/integration/authserver/
├── authserver_integration_test.go    # Main integration tests
├── runner_integration_test.go        # Runner-specific integration tests
├── README.md                         # This documentation
└── helpers/
    ├── authserver.go                 # Auth server test helpers
    ├── mock_upstream.go              # Mock upstream IDP
    └── http_client.go                # HTTP client helpers for OAuth flows
```

## Test Suites

### `authserver_integration_test.go`

| Test Suite | Coverage Area |
|------------|---------------|
| `TestEmbeddedAuthServer_DiscoveryEndpoints` | JWKS, OAuth metadata, OIDC discovery |
| `TestEmbeddedAuthServer_AuthorizationFlow` | Authorization initiation, redirect to upstream, RFC 8707 resource param |
| `TestEmbeddedAuthServer_DynamicClientRegistration` | DCR endpoint (RFC 7591) |
| `TestEmbeddedAuthServer_TokenEndpoint` | Token error handling, invalid grants |
| `TestEmbeddedAuthServer_ConfigurationValidation` | Config validation errors |
| `TestEmbeddedAuthServer_SigningKeyConfiguration` | Ephemeral keys, file-based keys |
| `TestEmbeddedAuthServer_ResourceCleanup` | Close idempotency |

### `runner_integration_test.go`

| Test Suite | Coverage Area |
|------------|---------------|
| `TestRunner_EmbeddedAuthServerIntegration` | Prefix handler mounting, MCP endpoint separation, concurrent requests |
| `TestRunner_CleanupClosesAuthServer` | Runner cleanup behavior |
| `TestRunner_AuthServerPrefixHandlersRoutingPriority` | Routing priority between auth and MCP |
| `TestRunner_AuthServerLifecycleWithContext` | Context cancellation handling |

## Running Tests

```bash
# Run all auth server integration tests
go test -v ./test/integration/authserver/...

# Run with race detection
go test -race -v ./test/integration/authserver/...

# Run specific test
go test -v -run TestEmbeddedAuthServer_DiscoveryEndpoints ./test/integration/authserver/...

# Run with coverage
go test -coverprofile=coverage.out ./test/integration/authserver/...
```

## Test Helpers

### `helpers/authserver.go`

Provides helpers for creating test auth server configurations:
- `NewTestAuthServerConfig()` - Creates minimal valid RunConfig
- `NewEmbeddedAuthServer()` - Creates and manages embedded auth server
- `GetFreePort()` - Returns available TCP port

### `helpers/mock_upstream.go`

Provides a mock OAuth2/OIDC upstream IDP:
- `NewMockUpstreamIDP()` - Creates mock upstream with default handlers
- Configurable handlers for authorization, token, and userinfo endpoints
- OIDC discovery endpoint support

### `helpers/http_client.go`

Provides OAuth client for testing flows:
- `NewOAuthClient()` - Creates non-redirecting HTTP client
- `GetJWKS()` - Fetches JWKS endpoint
- `GetOAuthDiscovery()` - Fetches OAuth AS Metadata
- `GetOIDCDiscovery()` - Fetches OIDC Discovery
- `StartAuthorization()` - Initiates authorization flow
- `ExchangeToken()` - Performs token exchange
- `RegisterClient()` - Performs dynamic client registration

## Design Decisions

### Mock Upstream IDP

Tests use a mock upstream IDP instead of a real OIDC provider to:
- Avoid external network dependencies
- Enable parallel test execution
- Provide predictable responses

### Development Mode Keys

Tests use ephemeral signing keys (development mode) by default to avoid:
- Managing key files in tests
- Complex setup procedures

Specific tests for file-based keys generate temporary keys.

### httptest.Server

Tests use Go's `httptest.Server` instead of real ports to:
- Enable parallel test execution
- Avoid port conflicts
- Simplify cleanup

## Important Code References

- Runner auth server integration: `pkg/runner/runner.go` lines 231-249
- Runner cleanup: `pkg/runner/runner.go` lines 702-710
- Embedded auth server factory: `pkg/authserver/runner/embeddedauthserver.go`
- Handler routes: `pkg/authserver/server/handlers/handler.go`

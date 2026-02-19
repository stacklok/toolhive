# Design: Registry Authentication

**Issue**: [#2962](https://github.com/stacklok/toolhive/issues/2962)
**Status**: Draft
**Author**: TBD
**Date**: 2026-02-18

## Summary

Add support for authenticating the ToolHive CLI to remote MCP server registries. Today, all registry HTTP requests (both static JSON and API endpoints) are unauthenticated. This prevents organizations from hosting private registries that require authentication.

## Motivation

Organizations hosting private MCP server registries need to restrict access to authorized users. The ToolHive CLI currently has no mechanism to attach credentials when communicating with remote registries. This means:

- Private registries behind authentication cannot be used
- Organizations cannot control who can discover their MCP servers

Meanwhile, ToolHive already has mature authentication infrastructure for remote MCP *servers* (`pkg/auth/remote/`) and a flexible HTTP client builder (`pkg/networking/http_client.go`) that supports bearer token injection. The gap is simply that the registry providers don't use it.

## Goals

1. Allow users to configure authentication credentials for remote registries
2. Support common auth mechanisms: bearer tokens, OAuth/OIDC
3. Store credentials securely using existing secrets infrastructure
4. Maintain backward compatibility (unauthenticated registries continue to work)
5. Provide a clean CLI experience for configuring registry auth

## Non-Goals

- Per-server authentication

## Current Architecture

### Registry Provider Chain

```
thv config set-registry <url>
        │
        ▼
┌─────────────────────┐
│  pkg/registry/      │
│  factory.go         │──▶ Priority: API > Remote > Local > Embedded
│                     │
│  NewRegistryProvider│
└────────┬────────────┘
         │
         ├──▶ CachedAPIRegistryProvider ──▶ api/client.go ──▶ HTTP GET (no auth)
         ├──▶ RemoteRegistryProvider    ──▶ HttpClientBuilder ──▶ HTTP GET (no auth)
         └──▶ LocalRegistryProvider     ──▶ (no network)
```

### Existing Auth Infrastructure

| Component | Location | Capabilities |
|-----------|----------|-------------|
| Remote auth config | `pkg/auth/remote/config.go` | OAuth/OIDC, bearer tokens, DCR, token caching |
| HTTP client builder | `pkg/networking/http_client.go` | Bearer token from file, CA bundles |
| Secrets manager | `pkg/secrets/` | Encrypted storage, 1Password, env vars |
| Config storage | `pkg/config/config.go` | YAML config with `UpdateConfig()` locking |

### Key Observation

The `HttpClientBuilder` already supports `.WithTokenFromFile(path)` which wraps the transport with `oauth2.Transport` for bearer token injection. The API client in `pkg/registry/api/client.go` builds an HTTP client but never uses this capability.

## Proposed Design

### Phased Approach

The implementation is split into two phases to keep PRs small and reviewable:

- **Phase 1**: Bearer token authentication (covers most private registry use cases)
- **Phase 2**: OAuth/OIDC authentication (for registries using standard identity providers)

### Phase 1: Bearer Token Authentication

#### Configuration Model

Add a `RegistryAuth` section to the existing config:

```yaml
# ~/.config/toolhive/config.yaml
registry_api_url: "https://registry.company.com"
registry_auth:
  type: "bearer"                    # "bearer", "oauth", or "" (none)
  bearer_token_ref: "REGISTRY_TOKEN"  # Reference to secret in secrets manager
  bearer_token_file: ""              # Alternative: path to token file
```

**Config struct** (`pkg/config/config.go`):

```go
type RegistryAuth struct {
    Type            string `yaml:"type,omitempty"`              // "bearer", "oauth", ""
    BearerTokenRef  string `yaml:"bearer_token_ref,omitempty"`  // Secret manager reference
    BearerTokenFile string `yaml:"bearer_token_file,omitempty"` // Path to token file
}
```

#### Token Resolution Order

When the registry provider needs a token, it resolves in this order:

1. **Environment variable** `TOOLHIVE_REGISTRY_AUTH_TOKEN` (highest priority, enables CI/CD)
2. **Token file** from `bearer_token_file` config field
3. **Secrets manager** via `bearer_token_ref` config field
4. **No auth** (if none configured)

This matches the pattern used by secrets provider selection (`pkg/config/config.go:GetProviderTypeWithEnv`).

#### Registry Auth Package

Create `pkg/registry/auth/` to encapsulate token resolution:

```go
// pkg/registry/auth/auth.go
package auth

// Config holds registry authentication configuration
type Config struct {
    Type            string // "bearer", "oauth", ""
    BearerTokenRef  string // Secret manager reference
    BearerTokenFile string // Path to token file
}

// TokenSource provides tokens for registry HTTP requests
type TokenSource interface {
    // Token returns the current token, or empty string if no auth configured
    Token(ctx context.Context) (string, error)
}

// NewTokenSource creates a TokenSource from the auth config.
// Resolution order: env var > token file > secrets manager > no auth
func NewTokenSource(cfg *Config) (TokenSource, error)
```

#### HTTP Client Integration

Modify the registry API client to accept an optional `TokenSource`:

```go
// pkg/registry/api/client.go

func NewClient(baseURL string, allowPrivateIp bool, tokenSource auth.TokenSource) (Client, error) {
    builder := networking.NewHttpClientBuilder().WithPrivateIPs(allowPrivateIp)

    // ... existing builder configuration ...

    httpClient, err := builder.Build()
    if err != nil {
        return nil, fmt.Errorf("failed to build HTTP client: %w", err)
    }

    // Wrap transport with auth if token source provided
    if tokenSource != nil {
        httpClient.Transport = &authTransport{
            base:   httpClient.Transport,
            source: tokenSource,
        }
    }

    return &mcpRegistryClient{
        baseURL:    baseURL,
        httpClient: httpClient,
        // ...
    }, nil
}
```

The `authTransport` is a thin `http.RoundTripper` wrapper that calls `tokenSource.Token(ctx)` on each request and sets the `Authorization: Bearer <token>` header. This is preferable to modifying `HttpClientBuilder` because:

- The token may need to be refreshed (for OAuth phase 2)
- The token resolution involves context (secrets manager, env vars)
- It keeps `HttpClientBuilder` simple and stateless

#### Provider Factory Changes

Thread auth config through the provider creation chain:

```go
// pkg/registry/factory.go

func NewRegistryProvider(cfg *config.Config) (Provider, error) {
    // Resolve auth configuration
    var tokenSource auth.TokenSource
    if cfg != nil && cfg.RegistryAuth.Type != "" {
        var err error
        tokenSource, err = auth.NewTokenSource(&cfg.RegistryAuth)
        if err != nil {
            return nil, fmt.Errorf("failed to configure registry authentication: %w", err)
        }
    }

    if cfg != nil && len(cfg.RegistryApiUrl) > 0 {
        provider, err := NewCachedAPIRegistryProvider(
            cfg.RegistryApiUrl, cfg.AllowPrivateRegistryIp, true, tokenSource,
        )
        // ...
    }
    if cfg != nil && len(cfg.RegistryUrl) > 0 {
        provider, err := NewRemoteRegistryProvider(
            cfg.RegistryUrl, cfg.AllowPrivateRegistryIp, tokenSource,
        )
        // ...
    }
    // Local providers don't need auth
    // ...
}
```

#### CLI Commands

**Option A (recommended): Extend `set-registry` with auth flags**

```bash
# Set registry with inline token (stored in secrets manager)
thv config set-registry https://registry.company.com --auth-token <token>

# Set registry with token from file
thv config set-registry https://registry.company.com --auth-token-file /path/to/token

# Set registry without auth (existing behavior, unchanged)
thv config set-registry https://registry.company.com
```

When `--auth-token` is provided:
1. Store the token in the secrets manager under a well-known key (e.g., `TOOLHIVE_REGISTRY_AUTH_TOKEN`)
2. Set `registry_auth.type = "bearer"` and `registry_auth.bearer_token_ref = "TOOLHIVE_REGISTRY_AUTH_TOKEN"` in config
3. Validate connectivity with the token before saving

When `--auth-token-file` is provided:
1. Validate the file exists and is readable
2. Set `registry_auth.type = "bearer"` and `registry_auth.bearer_token_file = <path>` in config
3. Validate connectivity with the token before saving

**Update `get-registry` output:**

```bash
$ thv config get-registry
Current registry: https://registry.company.com (API endpoint, authenticated)
```

**Update `unset-registry`:**

Clearing the registry also clears auth config. Optionally clean up the stored secret.

**New command for managing auth independently:**

```bash
# Update auth without changing registry URL
thv config set-registry-auth --token <token>
thv config set-registry-auth --token-file /path/to/token

# Remove auth but keep registry
thv config unset-registry-auth

# Environment variable (no config change needed)
TOOLHIVE_REGISTRY_AUTH_TOKEN=<token> thv registry list
```

#### Error Handling

When a registry returns `401 Unauthorized` or `403 Forbidden`:

```
Error: registry at https://registry.company.com returned 401 Unauthorized.

If this registry requires authentication, configure it with:
  thv config set-registry https://registry.company.com --auth-token <token>

Or set the TOOLHIVE_REGISTRY_AUTH_TOKEN environment variable.
```

This follows ToolHive's pattern of actionable error messages with hints.

### Phase 2: OAuth/OIDC Authentication

#### Motivation

Some registries (e.g., those behind corporate identity providers) use OAuth/OIDC rather than static tokens. The [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server) already supports enterprise OAuth 2.0/OIDC authentication.

#### Design

Extend `RegistryAuth` config to support OAuth:

```yaml
registry_auth:
  type: "oauth"
  oauth:
    issuer: "https://auth.company.com"
    client_id: "toolhive-cli"
    scopes: ["registry:read"]
    use_pkce: true
```

Reuse the existing OAuth infrastructure:

- `pkg/auth/oauth/oidc.go` for OIDC discovery
- `pkg/auth/remote/` patterns for token caching and refresh
- Secrets manager for storing refresh tokens (same pattern as `CachedRefreshTokenRef`)

**CLI flow:**

```bash
# Configure OAuth-authenticated registry
thv config set-registry https://registry.company.com \
    --auth-type oauth \
    --auth-issuer https://auth.company.com \
    --auth-client-id toolhive-cli

# First registry access triggers browser-based OAuth login
thv registry list
# → Opens browser for authentication
# → Caches tokens for future requests
```

The OAuth token source would implement the same `TokenSource` interface from Phase 1, making the registry providers agnostic to the auth mechanism.

#### Token Lifecycle

```
First request:
  TokenSource.Token() → no cached token → trigger OAuth flow → cache tokens → return access token

Subsequent requests:
  TokenSource.Token() → cached access token valid → return access token

Token expired:
  TokenSource.Token() → access token expired → use refresh token → return new access token

Refresh token expired:
  TokenSource.Token() → refresh failed → trigger OAuth flow → cache tokens → return access token
```

## Data Flow

### Phase 1 Bearer Token Flow

```
User: thv config set-registry https://registry.company.com --auth-token <token>
  │
  ▼
┌──────────────┐     ┌──────────────┐     ┌───────────────┐
│ CLI          │────▶│ Secrets Mgr  │────▶│ Encrypted     │
│ config.go    │     │ pkg/secrets/ │     │ Storage       │
└──────┬───────┘     └──────────────┘     └───────────────┘
       │
       ▼
┌──────────────┐
│ Config YAML  │  registry_auth:
│              │    type: bearer
│              │    bearer_token_ref: TOOLHIVE_REGISTRY_AUTH_TOKEN
└──────────────┘

User: thv registry list
  │
  ▼
┌──────────────┐     ┌──────────────┐     ┌───────────────┐
│ Registry     │────▶│ TokenSource  │────▶│ 1. Env var?   │
│ Factory      │     │              │     │ 2. Token file?│
│              │     │              │     │ 3. Secret mgr?│
└──────┬───────┘     └──────┬───────┘     └───────────────┘
       │                    │
       ▼                    ▼
┌──────────────┐     ┌──────────────┐
│ API Client   │────▶│ HTTP Request │──▶ Authorization: Bearer <token>
│              │     │ + authTransport│
└──────────────┘     └──────────────┘
```

## Implementation Plan

### PR 1: Config and Token Resolution (~150 LOC)

**Files:**
- `pkg/config/config.go` — Add `RegistryAuth` struct to `Config`
- `pkg/registry/auth/auth.go` — New package: `Config`, `TokenSource` interface, bearer token resolution
- `pkg/registry/auth/auth_test.go` — Unit tests for token resolution order

**Testing:**
- Env var takes precedence over file
- File takes precedence over secret ref
- Empty config returns nil token source
- Invalid token file returns error

### PR 2: Wire Auth into Registry Providers (~200 LOC)

**Files:**
- `pkg/registry/api/client.go` — Accept `TokenSource`, add `authTransport`
- `pkg/registry/provider_api.go` — Pass token source through
- `pkg/registry/provider_remote.go` — Pass token source through
- `pkg/registry/provider_cached.go` — Pass token source through
- `pkg/registry/factory.go` — Resolve auth config, create token source, pass to providers

**Testing:**
- API client sends `Authorization` header when token source configured
- API client works without auth (nil token source)
- 401/403 responses return actionable error messages
- Factory creates authenticated providers from config

### PR 3: CLI Commands (~150 LOC)

**Files:**
- `cmd/thv/app/config.go` — Add `--auth-token`, `--auth-token-file` flags to `set-registry`; add `set-registry-auth` and `unset-registry-auth` commands
- `pkg/registry/configurator.go` (or equivalent) — Business logic for setting auth config + storing token in secrets manager

**Testing:**
- E2E: `thv config set-registry <url> --auth-token <token>` stores config correctly
- E2E: `thv config get-registry` shows auth status
- E2E: `thv config unset-registry-auth` clears auth
- E2E: `TOOLHIVE_REGISTRY_AUTH_TOKEN=xxx thv registry list` uses env var

### PR 4: OAuth/OIDC Support (Phase 2, ~300 LOC)

**Files:**
- `pkg/registry/auth/oauth.go` — OAuth token source implementation reusing `pkg/auth/oauth/`
- `pkg/registry/auth/oauth_test.go` — Tests
- `cmd/thv/app/config.go` — OAuth-specific flags
- Config struct extensions for OAuth fields

**Testing:**
- Token caching works across CLI invocations
- Token refresh works when access token expires
- Browser-based flow triggers correctly

## Security Considerations

1. **Token storage**: Bearer tokens provided via `--auth-token` are stored in the secrets manager (encrypted at rest), not in plaintext config. The config only stores a *reference* to the secret.

2. **Token file permissions**: When using `--auth-token-file`, the CLI should warn if the file has overly permissive permissions (not 0600).

3. **Environment variable**: `TOOLHIVE_REGISTRY_AUTH_TOKEN` follows the existing pattern (`TOOLHIVE_SECRET_*`, `TOOLHIVE_REMOTE_AUTH_BEARER_TOKEN`) and is suitable for CI/CD where secrets are injected by the pipeline.

4. **No tokens in logs**: Token values must never appear in debug logs. Log token *presence* ("using bearer token from env var") not token *values*.

5. **HTTPS enforcement**: The existing `ValidatingTransport` in `HttpClientBuilder` already enforces HTTPS by default. Auth tokens will not be sent over plain HTTP unless `--allow-private-ip` is used (which also enables HTTP for localhost testing).

6. **Credential rotation**: Token file and env var approaches support rotation without CLI reconfiguration. Secret manager references support rotation via `thv secret set`.

## Alternatives Considered

### A. Embed auth in registry URL (e.g., `https://user:token@registry.com`)

**Rejected**: Credentials in URLs appear in logs, shell history, and process listings. Not secure.

### B. Always use secrets manager (no env var or file support)

**Rejected**: Requires `thv secret setup` before using authenticated registries. Env vars are essential for CI/CD. Token files are standard in cloud-native tooling (e.g., Kubernetes service account tokens).

### C. Add auth to `HttpClientBuilder` directly

**Rejected**: The builder is stateless and builds a client once. Registry auth may need dynamic token resolution (env vars, secrets manager lookups, OAuth refresh). A `TokenSource`-based `RoundTripper` is more flexible.

### D. Separate auth config file

**Rejected**: Adds complexity. The existing `config.yaml` already stores registry configuration and is the natural place for registry auth.

## Open Questions

1. **Secrets manager dependency**: Should `--auth-token` require secrets manager setup (`thv secret setup`), or should we fall back to storing the token in the config file with a warning? Current recommendation: require secrets manager, matching how remote server auth works.

2. **Token validation on set**: Should `thv config set-registry --auth-token` validate the token works (make an authenticated test request) before saving? Current recommendation: yes, matching how `set-registry` already validates connectivity.

3. **Per-registry auth (future)**: If ToolHive ever supports multiple registries, the auth config would need to be a map keyed by registry URL. The current single-registry model keeps things simple.

## References

- [MCP Registry API Specification](https://github.com/modelcontextprotocol/registry)
- [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server)
- [RFC 6750: Bearer Token Usage](https://tools.ietf.org/html/rfc6750)
- [RFC 8707: Resource Indicators for OAuth 2.0](https://tools.ietf.org/html/rfc8707)
- Existing auth patterns: `pkg/auth/remote/config.go`, `pkg/networking/http_client.go`

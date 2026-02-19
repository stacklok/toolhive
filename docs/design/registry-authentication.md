# Design: Registry Authentication

**Issue**: [#2962](https://github.com/stacklok/toolhive/issues/2962)
**Status**: Phase 1 Implemented
**Date**: 2026-02-18

## Summary

Add support for authenticating the ToolHive CLI to remote MCP server registries. Previously, all registry HTTP requests were unauthenticated. This prevents organizations from hosting private registries that require authentication.

Phase 1 (implemented) adds OAuth/OIDC authentication with PKCE — the CLI opens a browser for the user to log in, receives tokens via a local callback, and injects them into all subsequent registry API requests. Phase 2 (future) will add static bearer token support for CI/CD environments.

## Motivation

Organizations hosting private MCP server registries need to restrict access to authorized users. The ToolHive CLI previously had no mechanism to attach credentials when communicating with remote registries. This means:

- Private registries behind authentication cannot be used
- Organizations cannot control who can discover their MCP servers

ToolHive already has mature OAuth/OIDC infrastructure for remote MCP *servers* (`pkg/auth/oauth/`, `pkg/auth/remote/`) and a secrets manager for credential persistence (`pkg/secrets/`). The registry auth feature reuses this infrastructure.

## Goals

1. Allow users to authenticate to remote registries via browser-based OAuth/OIDC
2. Cache and refresh tokens transparently across CLI invocations
3. Store credentials securely using existing secrets infrastructure
4. Maintain backward compatibility (unauthenticated registries continue to work)
5. Provide a clean CLI experience for configuring registry auth

## Non-Goals

- Per-server authentication (auth is per-registry)
- Client secret management (Phase 1 uses public clients with PKCE only)

## Architecture

### Registry Provider Chain

```
thv config set-registry <url>
thv config set-registry-auth --issuer <url> --client-id <id>
        │
        ▼
┌─────────────────────┐
│  pkg/registry/      │
│  factory.go         │──▶ Priority: API > Remote > Local > Embedded
│                     │
│  NewRegistryProvider│──▶ resolveTokenSource() from config
└────────┬────────────┘
         │
         ├──▶ CachedAPIRegistryProvider ──▶ api/client.go ──▶ HTTP + auth.Transport
         ├──▶ RemoteRegistryProvider    ──▶ HttpClientBuilder ──▶ HTTP GET (no auth)
         └──▶ LocalRegistryProvider     ──▶ (no network)
```

### Key Components

| Component | Location | Role |
|-----------|----------|------|
| OAuth flow | `pkg/auth/oauth/flow.go` | Browser-based PKCE flow with local callback server |
| OIDC discovery | `pkg/auth/oauth/oidc.go` | Auto-discovers auth/token endpoints from issuer |
| Token source | `pkg/registry/auth/oauth_token_source.go` | Token lifecycle: cache → restore → browser flow |
| Auth transport | `pkg/registry/auth/transport.go` | Injects `Authorization: Bearer` on HTTP requests |
| Auth configurator | `pkg/registry/auth_configurator.go` | Business logic for set/unset/get auth config |
| Token persistence | `pkg/auth/remote/persisting_token_source.go` | Wraps token source to persist refresh tokens |
| Secrets manager | `pkg/secrets/` | Encrypted storage for refresh tokens |
| Config storage | `pkg/config/config.go` | YAML config with `RegistryAuth` section |

## Phase 1: OAuth/OIDC with PKCE (Implemented)

### Configuration Model

```yaml
# ~/.config/toolhive/config.yaml
registry_api_url: "https://registry.company.com"
registry_auth:
  type: "oauth"
  oauth:
    issuer: "https://auth.company.com"
    client_id: "toolhive-cli"
    scopes: ["openid"]
    audience: "api://my-registry"
    use_pkce: true
    callback_port: 8666
    # Populated automatically after first login:
    cached_refresh_token_ref: "REGISTRY_OAUTH_REFRESH_TOKEN"
    cached_token_expiry: "2026-02-20T12:00:00Z"
```

**Config structs** (`pkg/config/config.go`):

```go
type RegistryAuth struct {
    Type  string              `yaml:"type,omitempty"`   // "oauth" or "" (none)
    OAuth *RegistryOAuthConfig `yaml:"oauth,omitempty"`
}

type RegistryOAuthConfig struct {
    Issuer       string   `yaml:"issuer"`
    ClientID     string   `yaml:"client_id"`
    ClientSecret string   `yaml:"client_secret,omitempty"` // For confidential clients (optional)
    Scopes       []string `yaml:"scopes,omitempty"`
    Audience     string   `yaml:"audience,omitempty"`       // RFC 8707 resource indicator
    UsePKCE      bool     `yaml:"use_pkce"`
    CallbackPort int      `yaml:"callback_port,omitempty"`

    CachedRefreshTokenRef string    `yaml:"cached_refresh_token_ref,omitempty"`
    CachedTokenExpiry     time.Time `yaml:"cached_token_expiry,omitempty"`
}
```

### CLI Commands

**Setup:**

```bash
# Configure the registry URL
thv config set-registry https://registry.company.com/api

# Configure OAuth authentication
thv config set-registry-auth \
    --issuer https://auth.company.com \
    --client-id toolhive-cli \
    --audience api://my-registry

# View current configuration
thv config get-registry
# → Current registry: https://registry.company.com/api (API endpoint, OAuth configured)

# Remove auth (keeps registry URL)
thv config unset-registry-auth
```

**Flags for `set-registry-auth`:**

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--issuer` | Yes | — | OIDC issuer URL |
| `--client-id` | Yes | — | OAuth client ID |
| `--scopes` | No | `openid` | OAuth scopes (comma-separated) |
| `--audience` | No | — | OAuth audience / resource indicator |
| `--use-pkce` | No | `true` | Enable PKCE (recommended for public clients) |

The `set-registry-auth` command validates the issuer by performing OIDC discovery before saving the configuration. This catches typos and unreachable issuers early.

### Token Lifecycle

```
First request (no cached tokens):
  Token() → no in-memory token → no stored refresh token → browser OAuth flow
    → Opens browser to issuer's authorize endpoint (with PKCE challenge)
    → User authenticates in browser
    → Issuer redirects to http://localhost:8666/callback with auth code
    → Exchange code + PKCE verifier for access + refresh tokens
    → Persist refresh token in secrets manager
    → Return access token

Subsequent CLI invocations:
  Token() → no in-memory token → restore refresh token from secrets manager
    → Use refresh token to get new access token (silent, no browser)
    → Return access token

Within same process:
  Token() → in-memory token source → auto-refresh via oauth2 library
    → Return access token

Refresh token expired/revoked:
  Token() → restore fails → browser OAuth flow (same as first request)
```

### Data Flow

```
Setup:
  thv config set-registry-auth --issuer ... --client-id ...
    │
    ├──▶ OIDC Discovery (validates issuer)
    │
    └──▶ Save to config.yaml: registry_auth.type="oauth", registry_auth.oauth={...}

Runtime:
  thv registry list
    │
    ▼
  factory.go: resolveTokenSource(cfg)
    │
    ├──▶ cfg.RegistryAuth.Type == "oauth" → create oauthTokenSource
    │
    ▼
  NewCachedAPIRegistryProvider(url, allowPrivate, usePersistent, tokenSource)
    │
    ├──▶ Skip validation probe (auth requires user interaction)
    │
    ▼
  api/client.go: NewClient(url, allowPrivate, tokenSource)
    │
    ├──▶ Wrap HTTP transport with auth.Transport
    │
    ▼
  First HTTP request triggers tokenSource.Token(ctx)
    │
    ├──▶ Try in-memory cache → miss
    ├──▶ Try secrets manager (refresh token) → miss (first time)
    ├──▶ Browser OAuth flow:
    │      1. OIDC Discovery → auth + token endpoints
    │      2. Generate PKCE verifier/challenge
    │      3. Start local server on :8666
    │      4. Open browser → issuer authorize endpoint
    │      5. User authenticates
    │      6. Callback received with auth code
    │      7. Exchange code for tokens
    │      8. Persist refresh token → secrets manager
    │      9. Update config with token reference
    │
    └──▶ HTTP request sent with Authorization: Bearer <access_token>
```

### Design Decisions

**Why skip API validation when auth is configured:**
The API provider normally validates the endpoint on creation by making a test request. When OAuth is configured, this test request would trigger the browser flow within a 10-second timeout, which cannot complete. Instead, validation is deferred to the first real API call.

**Why PKCE without client secret:**
CLI applications are public clients — they cannot securely store a client secret. PKCE (RFC 7636) provides equivalent security without requiring a secret. The identity provider must be configured as a "Native App" (public client) to accept requests without client authentication.

**Why reuse `pkg/auth/oauth/` instead of a new implementation:**
ToolHive already has a battle-tested OAuth flow for remote MCP server authentication. The registry auth reuses the same `oauth.NewFlow`, `oauth.CreateOAuthConfigFromOIDC`, and `remote.NewPersistingTokenSource` functions, ensuring consistency and avoiding duplication.

**Why a separate `pkg/registry/auth/` package:**
Registry auth has different concerns than MCP server auth (different config model, different token persistence keys, different lifecycle). A separate package keeps the boundaries clean while reusing the underlying OAuth primitives.

## Phase 2: Bearer Token Authentication (Future)

### Motivation

CI/CD pipelines and automated environments cannot use browser-based OAuth flows. These environments need static bearer token support where a pre-obtained token is provided via environment variable, file, or secrets manager.

### Design

Extend the `TokenSource` interface with a bearer token implementation:

```yaml
registry_auth:
  type: "bearer"
  bearer_token_ref: "REGISTRY_TOKEN"     # Secret manager reference
  bearer_token_file: "/path/to/token"    # Alternative: path to token file
```

**Token resolution order:**

1. Environment variable `TOOLHIVE_REGISTRY_AUTH_TOKEN` (highest priority, enables CI/CD)
2. Token file from `bearer_token_file` config field
3. Secrets manager via `bearer_token_ref` config field

**CLI commands:**

```bash
# Set auth with inline token (stored in secrets manager)
thv config set-registry-auth --token <token>

# Set auth with token file
thv config set-registry-auth --token-file /path/to/token

# Environment variable (no config change needed)
TOOLHIVE_REGISTRY_AUTH_TOKEN=<token> thv registry list
```

The bearer token source implements the same `TokenSource` interface as the OAuth source, so the registry providers remain agnostic to the auth mechanism.

### Error Handling

When a registry returns `401 Unauthorized` or `403 Forbidden`:

```
Error: registry at https://registry.company.com returned 401 Unauthorized.

If this registry requires authentication, configure it with:
  thv config set-registry-auth --issuer <issuer-url> --client-id <client-id>

For CI/CD environments:
  thv config set-registry-auth --token <token>
  # or: TOOLHIVE_REGISTRY_AUTH_TOKEN=<token> thv registry list
```

## Security Considerations

1. **No client secrets in config**: Phase 1 uses public clients with PKCE. The `client_secret` field exists for future confidential client support but is not required.

2. **Refresh token storage**: Refresh tokens are stored in the secrets manager (encrypted at rest via keyring or 1Password), not in plaintext config. The config only stores a *reference* key (`cached_refresh_token_ref`).

3. **No tokens in logs**: Token values never appear in debug logs. Only token *presence* is logged (e.g., "using cached refresh token").

4. **HTTPS enforcement**: The existing `ValidatingTransport` in `HttpClientBuilder` enforces HTTPS by default. Access tokens are not sent over plain HTTP unless `--allow-private-ip` is used (which enables HTTP for localhost testing).

5. **PKCE protection**: The authorization code flow uses S256 PKCE challenges, preventing authorization code interception attacks even for public clients.

6. **Callback port**: The local callback server runs on `localhost:8666` and only accepts a single callback before shutting down, minimizing the attack surface.

## Alternatives Considered

### A. Bearer tokens first, OAuth second

**Not chosen**: The primary use case is corporate registries behind identity providers (Okta, Azure AD, etc.). Browser-based OAuth provides a better user experience ("just log in") compared to manually obtaining and rotating bearer tokens. Bearer tokens are deferred to Phase 2 for CI/CD use cases.

### B. Embed auth in registry URL (e.g., `https://user:token@registry.com`)

**Rejected**: Credentials in URLs appear in logs, shell history, and process listings.

### C. Add auth to `HttpClientBuilder` directly

**Rejected**: The builder is stateless and builds a client once. Registry auth needs dynamic token resolution (OAuth refresh, secrets manager lookups). A `TokenSource`-based `RoundTripper` is more flexible.

### D. Separate auth config file

**Rejected**: Adds complexity. The existing `config.yaml` already stores registry configuration and is the natural place for registry auth.

### E. Dynamic Client Registration (RFC 7591)

**Deferred**: Auto-registering the CLI as an OAuth client with the identity provider would eliminate manual client ID configuration. However, not all identity providers support DCR, and it adds complexity. May be revisited if multiple registries with different IdPs become common.

## References

- [MCP Registry API Specification](https://github.com/modelcontextprotocol/registry)
- [ToolHive Registry Server](https://github.com/stacklok/toolhive-registry-server)
- [RFC 7636: PKCE](https://tools.ietf.org/html/rfc7636)
- [RFC 8707: Resource Indicators for OAuth 2.0](https://tools.ietf.org/html/rfc8707)
- [RFC 6750: Bearer Token Usage](https://tools.ietf.org/html/rfc6750)
- Existing auth infrastructure: `pkg/auth/oauth/`, `pkg/auth/remote/`, `pkg/secrets/`
